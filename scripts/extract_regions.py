"""Region extraction for PDF documents: layer 1 (pdfplumber native text/tables)
and layer 2 (unstructured.io layout segmentation). Layer 3 (VLM) is handled
Go-side via the Ollama HTTP API and does not run here.

stdin:  {"pdf_base64": "<base64-encoded PDF bytes>"}
stdout: {"regions": [
    {
        "region_type": "native_text" | "native_table" | "figure" |
                       "unconfirmed_text" | "unconfirmed_table",
        "page_number": 1,
        "bbox": [x0, y0, x1, y1],    # fractional page coordinates in [0, 1]
        "text":         "...",        # set for native_text / native_table
        "image_base64": "...",        # set for figure / unconfirmed_* (PNG crop)
        "needs_vlm":    "describe" | "confirm_scan" | ""
    },
    ...
]}

region_type semantics:
  native_text      — extracted by pdfplumber; no VLM needed.
  native_table     — extracted by pdfplumber as a structured table; no VLM.
  figure           — classified as an image/diagram by unstructured; needs_vlm="describe".
  unconfirmed_text — pdfplumber found no text here and unstructured classified it
                     as text, but it may be scanned; needs_vlm="confirm_scan".
  unconfirmed_table— as above but classified as a table; needs_vlm="confirm_scan".
"""

import base64
import io
import json
import resource
import sys
import tempfile
import time
import os


def _load_libs():
    """Lazy-import heavy dependencies so the module can be imported without them
    in unit-test environments that just send JSON without calling main()."""
    import pdfplumber
    from unstructured.partition.pdf import partition_pdf
    from PIL import Image
    return pdfplumber, partition_pdf, Image


def _bbox_to_fractional(bbox, page_width, page_height):
    """Convert an absolute-coordinate bbox to fractional [0,1] page coords."""
    if page_width == 0 or page_height == 0:
        return [0.0, 0.0, 1.0, 1.0]
    x0, y0, x1, y1 = bbox
    return [
        max(0.0, min(1.0, x0 / page_width)),
        max(0.0, min(1.0, y0 / page_height)),
        max(0.0, min(1.0, x1 / page_width)),
        max(0.0, min(1.0, y1 / page_height)),
    ]


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


def _unstructured_bbox(element, page_width, page_height):
    """Extract a fractional bbox from an unstructured element's metadata."""
    try:
        coords = element.metadata.coordinates
        if coords is None:
            return [0.0, 0.0, 1.0, 1.0]
        pts = coords.points  # list of (x, y) tuples
        xs = [p[0] for p in pts]
        ys = [p[1] for p in pts]
        return _bbox_to_fractional(
            (min(xs), min(ys), max(xs), max(ys)),
            page_width, page_height,
        )
    except Exception:
        return [0.0, 0.0, 1.0, 1.0]


def extract_regions(pdf_bytes):
    """Main extraction logic: unstructured.io (single pass) for text and
    figure/scan classification, pdfplumber (scoped to tables only) for
    structured table extraction, and returns a list of region dicts ready
    for JSON serialisation.

    This used to run pdfplumber and unstructured.io as two fully
    independent parses of the same document -- pdfplumber for native
    text+tables, unstructured only for whatever pdfplumber found nothing
    for -- out of an unmeasured assumption that pdfplumber's extraction was
    meaningfully higher-fidelity. Measured directly against a real 412-page
    PDF (see the production ingestion incident writeup): unstructured's
    "fast" strategy text output had 99.9% word-level coverage against
    pdfplumber's -- the caution was unfounded for text, so pdfplumber is no
    longer used for it, cutting this down to one parse for that content.
    The same measurement found unstructured's "fast" strategy returns ZERO
    Table elements where pdfplumber found 21 tables (323 cells) on that
    same document -- a real, confirmed gap, not a minor quality
    difference -- so pdfplumber is kept, scoped specifically to
    extract_tables().
    """
    t_start = time.perf_counter()
    pdfplumber, partition_pdf, Image = _load_libs()

    regions = []
    page_count = 0
    text_types = ("Text", "NarrativeText", "Title", "ListItem", "Header", "Footer", "UncategorizedText")

    # Write PDF to a temp file; unstructured needs a file path.
    with tempfile.NamedTemporaryFile(suffix=".pdf", delete=False) as tmp:
        tmp.write(pdf_bytes)
        tmp_path = tmp.name

    try:
        # Text + figure/scan classification: one parse via unstructured.
        # "fast" strategy avoids heavy model dependencies on CPU-only workers.
        t_text_start = time.perf_counter()
        elements = partition_pdf(tmp_path, strategy="fast", include_page_breaks=False)
        t_text_end = time.perf_counter()
        print(f"extract_regions: unstructured (strategy=fast) in {t_text_end - t_text_start:.2f}s, {len(elements)} elements")

        # Group text-family elements by page so each page becomes one
        # native_text region, matching the shape pdfplumber's per-page
        # extract_text() used to produce -- downstream (chunking) has no
        # reason to know the extraction source changed.
        page_text = {}
        for el in elements:
            if type(el).__name__ in text_types:
                page_num = getattr(el.metadata, "page_number", 1) or 1
                page_text.setdefault(page_num, []).append(str(el))
        for page_num in sorted(page_text):
            text = "\n".join(page_text[page_num]).strip()
            if text:
                regions.append({
                    "region_type": "native_text",
                    "page_number": page_num,
                    "bbox": [0.0, 0.0, 1.0, 1.0],  # whole-page text region
                    "text": text,
                    "image_base64": "",
                    "needs_vlm": "",
                })

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

        # Figures / tables unstructured found that pdfplumber's dedicated
        # extraction didn't -- from the same unstructured pass above, no
        # second parse.
        for el in elements:
            el_type = type(el).__name__
            page_num = getattr(el.metadata, "page_number", 1) or 1

            if el_type in text_types:
                continue
            if el_type == "Table":
                # Only include if pdfplumber didn't already get it.
                table_text = el.text or ""
                if table_text.strip():
                    # Check if this table text is already present in any native_table region.
                    already_present = any(
                        r["region_type"] == "native_table" and table_text.strip() in r["text"]
                        for r in regions
                    )
                    if not already_present:
                        regions.append({
                            "region_type": "unconfirmed_table",
                            "page_number": page_num,
                            "bbox": [0.0, 0.0, 1.0, 1.0],
                            "text": "",
                            "image_base64": "",
                            "needs_vlm": "confirm_scan",
                        })
                continue
            if el_type == "Image":
                regions.append({
                    "region_type": "figure",
                    "page_number": page_num,
                    "bbox": [0.0, 0.0, 1.0, 1.0],
                    "text": "",
                    "image_base64": "",  # image cropping requires pdf2image; skip for now
                    "needs_vlm": "describe",
                })

    finally:
        try:
            os.unlink(tmp_path)
        except OSError:
            pass

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
