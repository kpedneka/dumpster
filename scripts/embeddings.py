"""Local embedding model for scripts/inference_service.py's /embeddings
endpoint.

Model choice: BAAI/bge-small-en-v1.5, via sentence-transformers. Picked for
this specific trade-off: small enough (~130MB, 384 dims) to fit comfortably
in the memory headroom left over once the entity-extraction and
region-classification models are also warm in the same process (see the
System Architecture page's Inference Service sub-page for the full memory
budget), while scoring meaningfully better than smaller alternatives
(e.g. all-MiniLM-L6-v2) on retrieval benchmarks — the thing this app
actually uses embeddings for.

BGE's documented convention: prepend a fixed instruction string to *query*
text before embedding it, but not to document/passage text. Skipping this
for queries measurably hurts retrieval quality with this model family. The
is_query flag on embed() exists specifically to apply it correctly — the
caller (the Go llm.Embedder adapter that will wire local embeddings into
the query and ingestion paths) is responsible for saying which case it's
in, since this module has no way to infer it from the text alone.
"""
import os

_MODEL_NAME = "BAAI/bge-small-en-v1.5"
_DIMS = 384
_QUERY_INSTRUCTION = "Represent this sentence for searching relevant passages: "


def load_embedder():
    """Loads the embedding model once. Call at process startup, not per
    request — construction includes loading the model weights.

    Also caps PyTorch's own CPU thread count when OMP_NUM_THREADS is set
    (see fly.inference.toml) -- PyTorch defaults to spawning one intra-op
    thread per CPU core it *detects*, which on a throttled/shared cloud
    vCPU allocation can wildly overshoot the CPU the container actually
    gets: the container can report far more logical CPUs than its cgroup
    quota grants. Measured directly in production: the exact same 433-text
    batch that took 47s locally (uncapped, real dedicated cores) took
    13m18s on Fly's shared-cpu-2x -- a ~17x blowup for identical CPU-bound
    work, and a much bigger hit than the ~3x slowdown region extraction
    (a different, non-torch CPU-bound step) saw on the same machine at the
    same time -- consistent with thread-scheduling thrashing from
    over-threading, not just "the CPU is slower." Left uncapped for local
    dev (the env var is only set in fly.inference.toml), where the default
    already performs fine."""
    threads = os.environ.get("OMP_NUM_THREADS")
    if threads:
        import torch

        torch.set_num_threads(int(threads))

    from sentence_transformers import SentenceTransformer

    return SentenceTransformer(_MODEL_NAME)


def dims():
    """Returns the embedding dimensionality, without needing a loaded model.
    Used to answer the /embeddings endpoint's dims field even for the
    (never-expected-in-practice) empty-input case."""
    return _DIMS


def embed(model, texts, is_query=False):
    """Returns one embedding (a list of floats) per input text, in order.
    Applies BGE's query instruction prefix when is_query is True; leaves
    document/passage text unprefixed. Embeddings are L2-normalized so
    downstream cosine-distance comparisons (pgvector) are well-behaved."""
    if not texts:
        return []
    inputs = [_QUERY_INSTRUCTION + t for t in texts] if is_query else list(texts)
    vectors = model.encode(inputs, normalize_embeddings=True)
    return [vector.tolist() for vector in vectors]
