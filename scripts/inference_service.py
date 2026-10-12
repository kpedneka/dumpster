"""Consolidated ML inference service.

Wraps CPU-bound Python capabilities behind one HTTP service so callers
don't embed their own warm Python subprocess. Today it's local
development's inference container (docker-compose), serving query-time
embeddings to cmd/api; in AWS the same model runs as the API task's
embed-sidecar. Region classification (scripts/batch_regions_job.py) and
entity extraction (scripts/batch_entity_job.py) run as AWS Batch jobs, not
here; this process never loads the PDF libraries or GLiNER/spacy.

Endpoints:
  POST /embeddings  — {"texts": [...], "is_query": bool} ->
                       {"embeddings": [[...], ...], "dims": int}
  GET  /healthz      — 200 only once the embedding model has loaded;
                       503 before that. In practice this is belt-and-braces:
                       the ASGI server doesn't accept connections until the
                       lifespan startup below returns, so no endpoint can
                       ever serve against a partially-warm process regardless.

Run with: uvicorn inference_service:app --host 0.0.0.0 --port 8000
"""
import time
from contextlib import asynccontextmanager

from fastapi import FastAPI
from fastapi.responses import JSONResponse
from pydantic import BaseModel

import embeddings

_pipelines = {}
_REQUIRED_PIPELINES = ("embed_model",)

@asynccontextmanager
async def lifespan(_app: FastAPI):
    # Loaded synchronously, in this order, before the ASGI server accepts
    # any connections. This ordering — not a separate readiness flag — is
    # what guarantees no endpoint ever serves against a partially-warm
    # process.
    embed_model = embeddings.load_embedder()
    _pipelines["embed_model"] = embed_model
    yield
    _pipelines.clear()


app = FastAPI(lifespan=lifespan)


@app.get("/healthz")
def healthz():
    if not all(k in _pipelines for k in _REQUIRED_PIPELINES):
        return JSONResponse(status_code=503, content={"status": "loading"})
    return {"status": "ok"}


class EmbedRequest(BaseModel):
    texts: list[str]
    is_query: bool = False


@app.post("/embeddings")
def embed(req: EmbedRequest):
    embed_start = time.monotonic()
    vectors = embeddings.embed(_pipelines["embed_model"], req.texts, is_query=req.is_query)
    print(f"/embeddings: {len(req.texts)} texts in {time.monotonic() - embed_start:.2f}s")
    return {"embeddings": vectors, "dims": embeddings.dims()}
