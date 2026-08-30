"""Consolidated ML inference service (v4.8).

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
                       Logic is reused verbatim via extract_regions() and
                       _peak_rss_kb(), not reimplemented.
  POST /embeddings  — new: {"texts": [...], "is_query": bool} ->
                       {"embeddings": [[...], ...], "dims": int}
  GET  /healthz      — 200 only once all three models have finished loading;
                       503 before that. In practice this is belt-and-braces:
                       the ASGI server doesn't accept connections until the
                       lifespan startup below returns, so no endpoint can
                       ever serve against a partially-warm process regardless.

Run with: uvicorn inference_service:app --host 0.0.0.0 --port 8000
"""
import base64
import json
from contextlib import asynccontextmanager

from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse
from pydantic import BaseModel

import embeddings
import extract_entities
import extract_regions

_pipelines = {}
_REQUIRED_PIPELINES = ("nlp", "entity_model", "embed_model")


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
        response_line = extract_entities._handle_request(
            _pipelines["nlp"], _pipelines["entity_model"], json.dumps(body)
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

    region_list = extract_regions.extract_regions(pdf_bytes)
    return {"regions": region_list, "peak_rss_kb": extract_regions._peak_rss_kb()}


class EmbedRequest(BaseModel):
    texts: list[str]
    is_query: bool = False


@app.post("/embeddings")
def embed(req: EmbedRequest):
    vectors = embeddings.embed(_pipelines["embed_model"], req.texts, is_query=req.is_query)
    return {"embeddings": vectors, "dims": embeddings.dims()}
