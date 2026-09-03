"""AWS Batch job entrypoint for GPU entity extraction.

Pure compute, same principle as every other Python component in this repo
(extract_entities.py, extract_regions.py, embeddings.py): this container
never touches Postgres or object storage credentials directly. The Go
worker uploads chunk data to object storage and hands this job a
short-lived presigned GET URL instead of real credentials -- the smallest
credential surface for something running on a separate cloud from the
rest of the app.

Environment variables:
  CHUNKS_URL — presigned GET URL for the input, a JSON object shaped
               exactly like extract_entities.py's stdin protocol:
               {"allowed_types": [...], "chunks": [{"chunk_id", "text"}, ...]}

Output contract: on success, the LAST line printed to stdout is
"BATCH_RESULT: " followed by the same JSON response extract_entities.py's
_handle_request() already produces ({"entities": [...]}). The worker reads
this back via CloudWatch Logs (AWS Batch's own log capture), not a second
storage round-trip. The marker prefix exists because _handle_request()
itself already prints a timing line to stdout (see extract_entities.py) --
without an unambiguous marker, the worker would have no reliable way to
tell that line apart from the actual result.

On failure, this exits non-zero (so Batch marks the job FAILED, which the
worker's polling already distinguishes from success) and prints
"BATCH_ERROR: " plus the exception, before the marker line would have
appeared.
"""
import json
import os
import sys
import urllib.request


def main():
    chunks_url = os.environ["CHUNKS_URL"]

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

    # Validate before declaring success -- a malformed response here would
    # otherwise look like success to the worker until it tries to parse it.
    json.loads(response_line)
    print(f"BATCH_RESULT: {response_line}", flush=True)


if __name__ == "__main__":
    main()
