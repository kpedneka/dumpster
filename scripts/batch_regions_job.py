"""AWS Batch job entrypoint for PDF region extraction.

Moved off the always-on Fly inference service's /regions endpoint for the
same reason ingestion-time embedding moved to Batch (see
batch_embed_job.py): region extraction only needs to run once per
document at ingest, so there's no reason to pay for an always-on service
to hold that capability warm between uploads. Unlike embedding, region
extraction (pymupdf + pdfplumber, see extract_regions.py) never loaded a
model in the first place -- there's no warm-state benefit being given up
here, only the idle-capacity cost of the always-on machine itself.

The isolated-subprocess memory cap extract_regions_isolated() adds for the
inference-service path (protecting a shared, long-lived process from one
pathological document) is unnecessary here: an AWS Batch job is already a
single-document, single-use container that can fail and exit without
endangering anything else, so this calls extract_regions() directly.

Same request/response transport as batch_embed_job.py: input and result
both travel via presigned S3 URLs, not stdin/stdout or CloudWatch Logs --
a real document's region text can run well past CloudWatch's ~256KB
single-event cap.

Environment variables:
  PDF_URL    — presigned GET URL for the raw PDF bytes (not base64-wrapped
               JSON -- the object itself is the PDF, unlike the local
               stdin/HTTP paths which pass pdf_base64 inside a JSON body).
  RESULT_URL — presigned PUT URL this job uploads its JSON result to on
               success: {"regions": [...], "peak_rss_kb": N} -- same shape
               extract_regions.py's own main() and the HTTP /regions
               endpoint already produce, for parity with both.

On success, a short confirmation line is printed to stdout; the actual
result lives at RESULT_URL. On failure, this exits non-zero and prints
"BATCH_ERROR: " plus the exception -- RESULT_URL is never written, so the
Go side's read of it simply fails.
"""
import json
import os
import sys
import urllib.request


def main():
    pdf_url = os.environ["PDF_URL"]
    result_url = os.environ["RESULT_URL"]

    try:
        with urllib.request.urlopen(pdf_url) as resp:
            pdf_bytes = resp.read()
    except Exception as exc:
        print(f"BATCH_ERROR: fetching PDF_URL: {exc}", flush=True)
        sys.exit(1)

    import extract_regions

    try:
        regions = extract_regions.extract_regions(pdf_bytes)
    except Exception as exc:
        print(f"BATCH_ERROR: region extraction failed: {exc}", flush=True)
        sys.exit(1)

    result_body = json.dumps({
        "regions": regions,
        "peak_rss_kb": extract_regions._peak_rss_kb(),
    }).encode("utf-8")
    try:
        req = urllib.request.Request(result_url, data=result_body, method="PUT")
        req.add_header("Content-Type", "application/json")
        with urllib.request.urlopen(req):
            pass
    except Exception as exc:
        print(f"BATCH_ERROR: uploading result: {exc}", flush=True)
        sys.exit(1)

    print(f"regions_job: uploaded {len(regions)} regions ({len(result_body)} bytes)", flush=True)


if __name__ == "__main__":
    main()
