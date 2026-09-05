"""One-off AWS Batch experiment: how long does CPU-only local embedding
actually take on genuinely dedicated compute (Fargate), cold start
included, compared to Fly's shared-cpu-2x machine?

Measured directly on Fly: 13-16+ minutes for this exact workload (433
texts, ~3000 bytes each) versus 47s on a real, unthrottled local machine.
Root cause confirmed against Fly's own docs: the shared-cpu tier throttles
to a 5ms/80ms baseline quota per vCPU once burst balance empties — a
scheduler-level CPU denial no in-process thread tuning can work around
(see fly.inference.toml's OMP_NUM_THREADS comment for the full story, and
why that fix alone didn't solve it). This script exists to measure whether
genuinely dedicated vCPU (Fargate has no burst-credit throttling model at
all) actually fixes it, and by how much, cold start included.

Not part of the real ingestion pipeline — reuses embeddings.py's actual
load_embedder()/embed() so the measured numbers reflect the real code
path, but generates synthetic text instead of taking real input, since
this is purely a timing experiment, not a job that needs to produce
usable output.

Prints one line per phase, flush=True, readable directly from the job's
CloudWatch log stream:
  embed_bench_job: process started at <ISO timestamp>
  embed_bench_job: model load took <N>s
  embed_bench_job: embedded <N> texts (<N>B each) in <N>s
  embed_bench_job: total (load+embed) <N>s
"""
import time
from datetime import datetime, timezone

# Matches chunk.DefaultFixedWindow() (internal/chunk/splitter.go): 3000-byte
# windows are the real chunk size this workload embeds in production, not
# an arbitrary round number picked for this benchmark.
_CHUNK_BYTES = 3000
_NUM_CHUNKS = 433  # the exact count from the real Wireshark PDF test run

# A small pool of varied, real-English sentences, cycled and concatenated
# up to _CHUNK_BYTES. Avoids a single repeated token (unrealistically cheap
# to tokenize/embed) while staying dependency-free — no lorem-ipsum package
# needed for a throwaway benchmark image.
_SENTENCE_POOL = [
    "The transmission control protocol establishes a reliable, ordered stream of bytes between two endpoints. ",
    "Wireshark captures packets at the link layer and reconstructs higher-level protocol state from them. ",
    "A dissector is responsible for interpreting the bytes of a single protocol layer within a captured frame. ",
    "Contributors are expected to add a changelog entry and a capture file demonstrating any new dissector. ",
    "The build system compiles the core library, the GUI, and the command-line tools from a shared CMake tree. ",
    "Debugging a crash in a dissector typically starts by reproducing it against a minimized capture file. ",
]


def _make_text(target_bytes: int) -> str:
    text = ""
    i = 0
    while len(text.encode("utf-8")) < target_bytes:
        text += _SENTENCE_POOL[i % len(_SENTENCE_POOL)]
        i += 1
    return text


def main():
    start = time.monotonic()
    print(f"embed_bench_job: process started at {datetime.now(timezone.utc).isoformat()}", flush=True)

    texts = [_make_text(_CHUNK_BYTES) for _ in range(_NUM_CHUNKS)]

    import embeddings

    load_start = time.monotonic()
    model = embeddings.load_embedder()
    load_elapsed = time.monotonic() - load_start
    print(f"embed_bench_job: model load took {load_elapsed:.2f}s", flush=True)

    embed_start = time.monotonic()
    vectors = embeddings.embed(model, texts)
    embed_elapsed = time.monotonic() - embed_start
    print(
        f"embed_bench_job: embedded {len(vectors)} texts ({_CHUNK_BYTES}B each) in {embed_elapsed:.2f}s",
        flush=True,
    )

    print(f"embed_bench_job: total (load+embed) {time.monotonic() - start:.2f}s", flush=True)


if __name__ == "__main__":
    main()
