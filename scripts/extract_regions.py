"""Region extraction for PDF documents: pymupdf (native text) and
pdfplumber (native tables). No image/figure or scanned-content
classification runs here — see extract_regions()'s docstring for why
unstructured.io, which used to provide that, was dropped entirely rather
than kept scoped to it.

stdin:  {"pdf_base64": "<base64-encoded PDF bytes>"}
stdout: {"regions": [
    {
        "region_type": "native_text" | "native_table",
        "page_number": 1,
        "bbox": [x0, y0, x1, y1],    # fractional page coordinates in [0, 1]
        "text":         "...",
        "image_base64": "",          # unused; kept for response-shape stability
        "needs_vlm":    ""           # unused, same reason
    },
    ...
]}

region_type semantics:
  native_text  — extracted by pymupdf; no VLM needed.
  native_table — extracted by pdfplumber as a structured table; no VLM.
"""

import base64
import io
import json
import resource
import sys
import time


def _load_libs():
    """Lazy-import heavy dependencies so the module can be imported without them
    in unit-test environments that just send JSON without calling main()."""
    import pdfplumber
    import pymupdf
    return pdfplumber, pymupdf


def _crop_page_region_as_png(page, frac_bbox, resolution=150):
    """Render the given fractional bbox on page as a base64 PNG string.
    Falls back to the full page image on error."""
    try:
        img = page.to_image(resolution=resolution).original
        w, h = img.size
        x0 = int(frac_bbox[0] * w)
        y0 = int(frac_bbox[1] * h)
        x1 = int(frac_bbox[2] * w)
        y1 = int(frac_bbox[3] * h)
        if x1 <= x0 or y1 <= y0:
            cropped = img
        else:
            cropped = img.crop((x0, y0, x1, y1))
        buf = io.BytesIO()
        cropped.save(buf, format="PNG")
        return base64.b64encode(buf.getvalue()).decode("utf-8")
    except Exception:
        return ""


def extract_regions(pdf_bytes):
    """Main extraction logic: pymupdf for native text, pdfplumber (scoped
    to tables only) for structured table extraction. Returns a list of
    region dicts ready for JSON serialisation.

    Text used to come from unstructured.io's "fast" strategy, itself a
    replacement for pdfplumber after measuring 99.9% word-level coverage
    against pdfplumber's extraction on a real 412-page PDF (see the
    production ingestion incident writeup) -- the fidelity caution around
    dropping pdfplumber for text was unfounded. Text now comes from pymupdf
    instead, for two independently confirmed reasons. First, speed: on
    that same 412-page PDF, pymupdf extracted text in 0.74s against
    unstructured's "fast" strategy's 41.42s, with 100% coverage of
    unstructured's own words (zero words unstructured found that pymupdf
    missed). Second, and more importantly, robustness: tested against a
    real 1345-page PDF with a malformed xref table, unstructured's "fast"
    strategy silently returned zero elements -- no exception, no
    warning -- meaning body text would have been dropped entirely from a
    real uploaded document with no signal anywhere that it happened.
    pdfminer (the library unstructured's "fast" strategy is itself built
    on) parses that same file's text without issue, so the failure is
    specific to unstructured's own partitioning layer. pymupdf extracted
    the full document correctly.

    unstructured was kept around for one release scoped to figure
    classification and a Table-element fallback for tables pdfplumber's
    stricter extract_tables() might have missed, on the reasoning that
    those roles hadn't been measured against an alternative so shouldn't
    be assumed safe to change too. That reasoning didn't hold: the same
    1345-page malformed PDF that silently dropped zero-element text also
    zeroed out figure/table-fallback classification, for the identical
    root cause -- unstructured's "fast" strategy failing silently on that
    document, not something specific to text. Keeping unstructured "just
    for these two roles" wasn't actually a robust fallback; it carried the
    exact same failure mode being fixed, on top of remaining the dominant
    cost in the whole pipeline (~41s of ~57s total on the 412-page PDF).
    unstructured is dropped entirely as of this change. The real cost:
    figures/diagrams are no longer detected or flagged for a VLM
    description, and pdfplumber's table extraction (structural; misses
    borderless or scanned tables) no longer has a second-opinion fallback.
    Accepted as a real feature loss, not a hidden one, given the app's
    current stage doesn't need that coverage.

    Tables: pdfplumber's "fast" unstructured strategy returns ZERO Table
    elements where pdfplumber found 21 tables (323 cells) on the same
    412-page PDF -- a real, confirmed gap, not a minor quality
    difference -- so pdfplumber is kept, scoped specifically to
    extract_tables().
    """
    t_start = time.perf_counter()
    pdfplumber, pymupdf = _load_libs()

    regions = []
    page_count = 0

    # Text: pymupdf, one parse. See docstring above for why this replaced
    # unstructured's "fast" strategy.
    t_text_start = time.perf_counter()
    text_doc = pymupdf.open(stream=pdf_bytes, filetype="pdf")
    for page_idx in range(len(text_doc)):
        text = text_doc[page_idx].get_text().strip()
        if text:
            regions.append({
                "region_type": "native_text",
                "page_number": page_idx + 1,
                "bbox": [0.0, 0.0, 1.0, 1.0],  # whole-page text region
                "text": text,
                "image_base64": "",
                "needs_vlm": "",
            })
    t_text_end = time.perf_counter()
    print(f"extract_regions: pymupdf text in {t_text_end - t_text_start:.2f}s, {len(text_doc)} pages")

    # Tables: pdfplumber, scoped to extract_tables() only -- the one
    # capability with a confirmed fidelity gap (see docstring above).
    t_tables_start = time.perf_counter()
    with pdfplumber.open(io.BytesIO(pdf_bytes)) as pdf:
        page_count = len(pdf.pages)
        for page_idx, page in enumerate(pdf.pages):
            page_num = page_idx + 1
            for table in page.extract_tables() or []:
                rows = []
                for row in table:
                    rows.append(" | ".join(str(cell) if cell is not None else "" for cell in row))
                table_text = "\n".join(rows)
                if table_text.strip():
                    regions.append({
                        "region_type": "native_table",
                        "page_number": page_num,
                        "bbox": [0.0, 0.0, 1.0, 1.0],
                        "text": table_text.strip(),
                        "image_base64": "",
                        "needs_vlm": "",
                    })
    t_tables_end = time.perf_counter()
    print(f"extract_regions: pdfplumber tables-only, {page_count} pages in {t_tables_end - t_tables_start:.2f}s")

    print(f"extract_regions: total {time.perf_counter() - t_start:.2f}s for {page_count} pages, {len(regions)} regions, peak_rss={_peak_rss_kb()}KB")
    return regions


def _peak_rss_kb():
    """Returns this process's peak resident set size in KB, for sizing
    decisions (see v4.6): the fixed OOM floor observed in production came
    from this exact number, but only for jobs that survive to report it —
    a killed job's peak RSS is only visible via the OOM killer's own log
    line, not this function. ru_maxrss is already KB on Linux (the
    production runtime) but bytes on macOS/BSD, so normalize for local dev.
    """
    raw = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss
    return raw // 1024 if sys.platform == "darwin" else raw


def main():
    req = json.load(sys.stdin)
    pdf_b64 = req.get("pdf_base64", "")
    if not pdf_b64:
        json.dump({"regions": [], "peak_rss_kb": _peak_rss_kb()}, sys.stdout)
        return

    pdf_bytes = base64.b64decode(pdf_b64)
    regions = extract_regions(pdf_bytes)
    json.dump({"regions": regions, "peak_rss_kb": _peak_rss_kb()}, sys.stdout)


if __name__ == "__main__":
    main()
