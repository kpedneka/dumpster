"""Query-time embedding sidecar for cmd/api.

Runs as a second container inside api's own ECS task (see
terraform/ecs_api.tf), reached over localhost rather than a network hop —
this replaces the /embeddings half of the retired standalone
inference_service.py. The /regions half doesn't move here: PDF/image
region classification folds into the AWS Batch embed job instead (see
scripts/batch_embed_job.py), a separate, later piece of work. Keeping
those two capabilities apart on purpose, not just for now: this process
never imports extract_regions or its pdfplumber/pymupdf dependencies at
all, so the sidecar's image stays exactly as heavy as the embedding model
itself needs and no heavier, and duplicating it per api replica (see the
Cost Comparison page's explicit tradeoff on that) doesn't also duplicate
PDF-parsing weight no query ever touches.

Endpoints:
  POST /embeddings — {"texts": [...], "is_query": bool} ->
                      {"embeddings": [[...], ...], "dims": int}
  GET  /healthz     — 200 only once the embedding model has finished
                      loading; 503 before that. Belt-and-braces, same as
                      inference_service.py: the ASGI server doesn't accept
                      connections until the lifespan startup below
                      returns, so no request can ever arrive against a
                      partially-warm process regardless.

Run with: uvicorn embed_service:app --host 0.0.0.0 --port 8000
"""
import time
from contextlib import asynccontextmanager

from fastapi import FastAPI
from fastapi.responses import JSONResponse
from pydantic import BaseModel

import embeddings

_pipelines = {}


@asynccontextmanager
async def lifespan(_app: FastAPI):
    _pipelines["embed_model"] = embeddings.load_embedder()
    yield
    _pipelines.clear()


app = FastAPI(lifespan=lifespan)


@app.get("/healthz")
def healthz():
    if "embed_model" not in _pipelines:
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
