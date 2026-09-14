"""AWS Batch job entrypoint for CPU-only local embedding.

Moved off the always-on Fly inference service's /embeddings endpoint after
measuring it there directly: Fly's shared-cpu tier throttles to a 5ms/80ms
baseline CPU quota once burst balance empties (confirmed against Fly's own
docs), which turned a 47.5s embedding call on a real machine into 13-16+
minutes in production -- a scheduler-level CPU denial no in-process thread
tuning could fix. AWS Batch on Fargate gives a genuinely dedicated vCPU
allocation instead, with no burst-credit model at all. See
scripts/embed_bench_job.py (the throwaway experiment that established
this) for the full numbers across several vCPU sizes.

Same design as scripts/batch_entity_job.py (entity extraction's AWS Batch
job): this container never gets real object-storage credentials -- the Go
worker hands it short-lived presigned URLs instead, and the RESULT goes
back via a presigned PUT to object storage, not printed to stdout for
CloudWatch Logs pickup. That log-based approach was the first design
tried here, and it broke in production: a real document's embedding
response (hundreds of texts x 384 floats, full JSON float precision) runs
a few MB, but CloudWatch Logs caps a single log event at ~256KB (since
raised to 1MB) and AWS's log driver silently splits an oversized print()
line across multiple events with no marker that they were ever one line
-- the Go side's log reassembly (built for the normal one-event-per-line
case) then produces corrupted, unparseable JSON. Entity extraction's
response was assumed small enough this would never affect it too, until
it did -- see internal/entity/awsbatch/awsbatch.go's package doc for that
story; it's since moved to this exact same S3-output design.

Environment variables:
  TEXTS_URL  — presigned GET URL for the input:
               {"texts": [...]}
               Always ingestion-time (document/passage) text -- query-time
               search embedding stays on the always-on Fly inference
               service (a per-request Fargate cold start would be
               unacceptable for a live search), so there is no is_query
               flag here at all.
  RESULT_URL — presigned PUT URL this job uploads its JSON result to on
               success: {"embeddings": [[...], ...], "dims": N} -- same
               shape as the HTTP /embeddings endpoint's response, for
               parity.

On success, nothing meaningful is printed to stdout beyond a short
confirmation line -- the actual result lives at RESULT_URL, not in logs.
On failure, this exits non-zero and prints "BATCH_ERROR: " plus the
exception; RESULT_URL is never written, so the Go side's read of it
simply fails, which is how it learns something went wrong beyond
whatever AWS Batch's own job status already says.
"""
import json
import os
import sys
import urllib.request


def main():
    texts_url = os.environ["TEXTS_URL"]
    result_url = os.environ["RESULT_URL"]

    try:
        with urllib.request.urlopen(texts_url) as resp:
            request_body = resp.read().decode("utf-8")
    except Exception as exc:
        print(f"BATCH_ERROR: fetching TEXTS_URL: {exc}", flush=True)
        sys.exit(1)

    try:
        texts = json.loads(request_body)["texts"]
    except Exception as exc:
        print(f"BATCH_ERROR: parsing input: {exc}", flush=True)
        sys.exit(1)

    import embeddings

    try:
        model = embeddings.load_embedder()
        vectors = embeddings.embed(model, texts, is_query=False)
    except Exception as exc:
        print(f"BATCH_ERROR: embedding failed: {exc}", flush=True)
        sys.exit(1)

    result_body = json.dumps({"embeddings": vectors, "dims": embeddings.dims()}).encode("utf-8")
    try:
        req = urllib.request.Request(result_url, data=result_body, method="PUT")
        req.add_header("Content-Type", "application/json")
        with urllib.request.urlopen(req):
            pass
    except Exception as exc:
        print(f"BATCH_ERROR: uploading result: {exc}", flush=True)
        sys.exit(1)

    print(f"embed_job: uploaded {len(vectors)} embeddings ({len(result_body)} bytes)", flush=True)


if __name__ == "__main__":
    main()
