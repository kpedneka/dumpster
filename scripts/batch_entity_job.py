"""AWS Batch job entrypoint for GPU entity extraction.

Pure compute, same principle as every other Python component in this repo
(extract_entities.py, extract_regions.py, embeddings.py): this container
never touches Postgres or object storage credentials directly. The Go
worker uploads chunk data to object storage and hands this job a
short-lived presigned GET URL for its input and a presigned PUT URL for
its result, instead of real credentials -- the smallest credential surface
for something running on a separate cloud from the rest of the app.

Environment variables:
  CHUNKS_URL — presigned GET URL for the input, a JSON object shaped
               exactly like extract_entities.py's stdin protocol:
               {"allowed_types": [...], "chunks": [{"chunk_id", "text"}, ...]}
  RESULT_URL — presigned PUT URL this job uploads its JSON result to on
               success: the same {"entities": [...]} shape
               extract_entities.py's _handle_request() already produces.

This used to print the result as a "BATCH_RESULT: " line to stdout instead,
read back by the worker via CloudWatch Logs rather than a second storage
round trip -- the same design scripts/batch_embed_job.py originally tried
and moved off of, for the same reason: GetLogEvents caps a single page at
1MB with no pagination on the Go side, and BATCH_RESULT was always the
last line printed, so it was specifically the part that went missing first
once a job's total output crossed that cap. Confirmed this wasn't
hypothetical for entity extraction either -- it was hit for real, before
per-batch chunking existed, when a whole document's entities accumulated
into one oversized result. See internal/entity/awsbatch/awsbatch.go's
package doc for the full story.

On success, nothing meaningful is printed to stdout beyond a short
confirmation line -- the actual result lives at RESULT_URL, not in logs.
On failure, this exits non-zero (so Batch marks the job FAILED, which the
worker's polling already distinguishes from success) and prints
"BATCH_ERROR: " plus the exception; RESULT_URL is never written, so the Go
side's read of it simply fails, which is how it learns something went
wrong beyond whatever AWS Batch's own job status already says.
"""
import json
import os
import sys
import urllib.request


def main():
    chunks_url = os.environ["CHUNKS_URL"]
    result_url = os.environ["RESULT_URL"]

    try:
        with urllib.request.urlopen(chunks_url) as resp:
            request_body = resp.read().decode("utf-8")
    except Exception as exc:
        print(f"BATCH_ERROR: fetching CHUNKS_URL: {exc}", flush=True)
        sys.exit(1)

    import extract_entities

    nlp, model = extract_entities.load_pipeline()

    import torch
    if torch.cuda.is_available():
        model = model.to("cuda")
        print(f"batch_entity_job: model on {torch.cuda.get_device_name(0)}", flush=True)
    else:
        print("batch_entity_job: WARNING — CUDA not available, running on CPU", flush=True)

    try:
        response_line = extract_entities._handle_request(nlp, model, request_body)
    except Exception as exc:
        print(f"BATCH_ERROR: extraction failed: {exc}", flush=True)
        sys.exit(1)

    # Validate before uploading -- a malformed response here would
    # otherwise look like success to the worker until it tries to parse
    # what it fetched back from RESULT_URL.
    entity_count = len(json.loads(response_line)["entities"])
    result_body = response_line.encode("utf-8")

    try:
        req = urllib.request.Request(result_url, data=result_body, method="PUT")
        req.add_header("Content-Type", "application/json")
        with urllib.request.urlopen(req):
            pass
    except Exception as exc:
        print(f"BATCH_ERROR: uploading result: {exc}", flush=True)
        sys.exit(1)

    print(f"entity_job: uploaded {entity_count} entities ({len(result_body)} bytes)", flush=True)


if __name__ == "__main__":
    main()
