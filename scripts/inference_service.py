"""Consolidated ML inference service.

Wraps three CPU-bound Python capabilities behind one always-on HTTP
service, so cmd/api and cmd/worker call over the network instead of each
embedding its own warm Python subprocess (the pattern this replaces — see
the System Architecture page's Inference Service sub-page for the full
design and why it changed).

Endpoints:
  POST /entities    — same request/response contract as extract_entities.py's
                       stdin/stdout protocol (see that file's docstring).
                       Logic is reused verbatim via _handle_request, not
                       reimplemented.
  POST /regions      — same request/response contract as extract_regions.py's
                       stdin/stdout protocol (see that file's docstring).
                       Runs in a separate, memory-capped child process per
                       call via extract_regions_isolated(), not directly in
                       this one — see that function's docstring. At most
                       REGIONS_MAX_CONCURRENCY (default 1) calls run at
                       once — see _REGIONS_SEMAPHORE's comment.
  POST /embeddings  — new: {"texts": [...], "is_query": bool} ->
                       {"embeddings": [[...], ...], "dims": int}
  GET  /healthz      — 200 only once all three models have finished loading;
                       503 before that. In practice this is belt-and-braces:
                       the ASGI server doesn't accept connections until the
                       lifespan startup below returns, so no endpoint can
                       ever serve against a partially-warm process regardless.

/entities and /regions dispatch their actual (slow, CPU-bound) work via
run_in_threadpool rather than calling it directly. uvicorn runs a single,
single-threaded event loop by default; a synchronous multi-second call made
directly in one of these coroutines would stop that one thread from making
progress on anything else at all — not just this request, every request,
including an unrelated /embeddings call for a search query — for its whole
duration. See each handler's inline comment for the specifics.

Run with: uvicorn inference_service:app --host 0.0.0.0 --port 8000
"""
import asyncio
import base64
import json
import os
from contextlib import asynccontextmanager

from fastapi import FastAPI, Request
from fastapi.concurrency import run_in_threadpool
from fastapi.responses import JSONResponse
from pydantic import BaseModel

import embeddings
import extract_entities
import extract_regions

_pipelines = {}
_REQUIRED_PIPELINES = ("nlp", "entity_model", "embed_model")

# Caps how many /regions requests run their extract_regions() call at once.
# A single large PDF's table extraction has been measured peaking at ~3.1GB
# RSS on its own (a 412-page document); this process's whole memory budget
# is 4096mb in production (see fly.inference.toml — that comment's "~1.3GB
# headroom" figure predates this measurement and is already stale). Two such
# requests running concurrently is what actually took the process down
# during local testing, not raw CPU/RAM scarcity — nothing here queued or
# rejected the second request, so both ran at once and stacked their peaks.
# Defaults to 1: on the current memory budget, running even two large PDFs
# concurrently isn't safe. Override via REGIONS_MAX_CONCURRENCY once a real
# per-request memory budget justifies raising it.
_REGIONS_SEMAPHORE = asyncio.Semaphore(int(os.environ.get("REGIONS_MAX_CONCURRENCY", "1")))


@asynccontextmanager
async def lifespan(_app: FastAPI):
    # Loaded synchronously, in this order, before the ASGI server accepts
    # any connections. This ordering — not a separate readiness flag — is
    # what guarantees no endpoint ever serves against a partially-warm
    # process.
    nlp, entity_model = extract_entities.load_pipeline()
    extract_regions._load_libs()
    embed_model = embeddings.load_embedder()
    _pipelines["nlp"] = nlp
    _pipelines["entity_model"] = entity_model
    _pipelines["embed_model"] = embed_model
    yield
    _pipelines.clear()


app = FastAPI(lifespan=lifespan)


@app.get("/healthz")
def healthz():
    if not all(k in _pipelines for k in _REQUIRED_PIPELINES):
        return JSONResponse(status_code=503, content={"status": "loading"})
    return {"status": "ok"}


def _parse_json_object(raw: bytes) -> dict:
    """Parses raw request bytes as a JSON object, raising ValueError for
    anything else (invalid JSON, or valid JSON that isn't an object) so
    callers can turn it into a single, consistent 400 response."""
    try:
        body = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise ValueError(f"invalid JSON: {exc}") from exc
    if not isinstance(body, dict):
        raise ValueError("request body must be a JSON object")
    return body


@app.post("/entities")
async def entities(request: Request):
    try:
        body = _parse_json_object(await request.body())
        # run_in_threadpool, not a direct call: _handle_request runs spaCy +
        # GLiNER inference, which can take seconds for a large batch of
        # chunks. Calling it directly here would block this process's one
        # asyncio event loop for that whole duration — uvicorn is
        # single-threaded by default, so every other in-flight request
        # (including an unrelated /embeddings call for a search query)
        # would simply stop making progress until this one call returned,
        # not just slow down. The threadpool dispatch is the same mechanism
        # FastAPI already uses automatically for a plain `def` endpoint
        # (see /embeddings below) — using it explicitly here keeps this
        # async endpoint (needed for `await request.body()`, for the
        # custom validation-friendly JSON parsing above) from being worse
        # off than a sync one for the actual CPU-bound work.
        response_line = await run_in_threadpool(
            extract_entities._handle_request, _pipelines["nlp"], _pipelines["entity_model"], json.dumps(body)
        )
    except (ValueError, KeyError, TypeError) as exc:
        return JSONResponse(status_code=400, content={"error": str(exc)})
    return JSONResponse(content=json.loads(response_line))


@app.post("/regions")
async def regions(request: Request):
    try:
        body = _parse_json_object(await request.body())
    except ValueError as exc:
        return JSONResponse(status_code=400, content={"error": str(exc)})

    pdf_b64 = body.get("pdf_base64", "")
    if not pdf_b64:
        return {"regions": [], "peak_rss_kb": extract_regions._peak_rss_kb()}

    try:
        pdf_bytes = base64.b64decode(pdf_b64, validate=True)
    except (base64.binascii.Error, ValueError) as exc:
        return JSONResponse(status_code=400, content={"error": f"invalid pdf_base64: {exc}"})

    # See the matching comment in /entities above: PDF layout classification
    # is the heaviest call in this whole service (can run for seconds to
    # tens of seconds on a large document) and must not run directly on the
    # event loop. The semaphore acquire below queues this request behind any
    # already-running /regions call rather than letting both run at once —
    # see _REGIONS_SEMAPHORE's comment for why. A queued request just waits;
    # the caller (internal/manifest/layout, called from a queue job with its
    # own retry/backoff and stale-job reclaim as backstops) doesn't need any
    # special handling for that.
    #
    # extract_regions_isolated, not extract_regions directly: the actual
    # extraction runs in a separate, memory-capped child process (see its
    # docstring). The semaphore above bounds how many such requests run at
    # once; this bounds how much memory any single one can use — the two
    # are complementary, not redundant. A document that blows past its
    # child's memory limit fails cleanly here (500) instead of taking this
    # whole process, and every other in-flight request, down with it.
    async with _REGIONS_SEMAPHORE:
        try:
            result = await run_in_threadpool(extract_regions.extract_regions_isolated, pdf_bytes)
        except RuntimeError as exc:
            return JSONResponse(status_code=500, content={"error": str(exc)})
    return {"regions": result["regions"], "peak_rss_kb": result["peak_rss_kb"]}


class EmbedRequest(BaseModel):
    texts: list[str]
    is_query: bool = False


@app.post("/embeddings")
def embed(req: EmbedRequest):
    vectors = embeddings.embed(_pipelines["embed_model"], req.texts, is_query=req.is_query)
    return {"embeddings": vectors, "dims": embeddings.dims()}
