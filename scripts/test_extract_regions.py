"""Unit tests for extract_regions.py.

Mocks pymupdf/pdfplumber (via _load_libs) so this runs fast without those
heavy dependencies installed, same pattern as test_inference_service.py
and test_gpu_entity_service.py. Run with:
    python3 -m unittest scripts/test_extract_regions.py
"""
import multiprocessing
import unittest
from unittest import mock

import extract_regions


class FakePage:
    def __init__(self, text="", tables=None):
        self._text = text
        self._tables = tables or []
        self.flush_cache_calls = 0

    def get_text(self):
        return self._text

    def extract_tables(self):
        return self._tables

    def flush_cache(self):
        self.flush_cache_calls += 1


class FakePyMuPDFDoc(list):
    """A fake pymupdf.Document: indexable/iterable/len()-able, matching how
    extract_regions() reads it (len(text_doc), text_doc[page_idx])."""


class FakePDFPlumberDoc:
    def __init__(self, pages):
        self.pages = pages

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False


class ExtractRegionsTestCase(unittest.TestCase):
    """Base class: patches _load_libs to return fake pymupdf/pdfplumber,
    configurable per test via self.pymupdf_pages, self.pdfplumber_pages."""

    def setUp(self):
        self.pymupdf_pages = [FakePage(text="page 1 text")]
        self.pdfplumber_pages = []

        self._stack = mock.patch.object(extract_regions, "_load_libs")
        self.mock_load_libs = self._stack.start()
        self.addCleanup(self._stack.stop)
        self.mock_load_libs.side_effect = self._fake_load_libs

    def _fake_load_libs(self):
        fake_pymupdf = mock.Mock()
        fake_pymupdf.open.side_effect = lambda **kwargs: FakePyMuPDFDoc(self.pymupdf_pages)

        fake_pdfplumber = mock.Mock()
        fake_pdfplumber.open.side_effect = lambda *a, **kw: FakePDFPlumberDoc(self.pdfplumber_pages)

        return fake_pdfplumber, fake_pymupdf


class TextExtractionTests(ExtractRegionsTestCase):
    def test_text_comes_from_pymupdf(self):
        self.pymupdf_pages = [FakePage(text="pymupdf's text")]
        regions = extract_regions.extract_regions(b"fake pdf bytes")

        text_regions = [r for r in regions if r["region_type"] == "native_text"]
        self.assertEqual(len(text_regions), 1)
        self.assertEqual(text_regions[0]["text"], "pymupdf's text")
        self.assertEqual(text_regions[0]["page_number"], 1)

    def test_blank_page_produces_no_native_text_region(self):
        self.pymupdf_pages = [FakePage(text="   \n  ")]
        regions = extract_regions.extract_regions(b"fake pdf bytes")
        self.assertEqual([r for r in regions if r["region_type"] == "native_text"], [])

    def test_multiple_pages_each_get_their_own_region(self):
        self.pymupdf_pages = [FakePage(text="page one"), FakePage(text="page two")]
        regions = extract_regions.extract_regions(b"fake pdf bytes")
        text_regions = sorted(
            (r for r in regions if r["region_type"] == "native_text"), key=lambda r: r["page_number"]
        )
        self.assertEqual([r["page_number"] for r in text_regions], [1, 2])
        self.assertEqual([r["text"] for r in text_regions], ["page one", "page two"])


class TablesTests(ExtractRegionsTestCase):
    def test_tables_come_from_pdfplumber(self):
        self.pdfplumber_pages = [FakePage(tables=[[["a", "b"], ["1", "2"]]])]
        regions = extract_regions.extract_regions(b"fake pdf bytes")

        table_regions = [r for r in regions if r["region_type"] == "native_table"]
        self.assertEqual(len(table_regions), 1)
        self.assertEqual(table_regions[0]["text"], "a | b\n1 | 2")

    def test_table_with_no_rows_is_skipped(self):
        self.pdfplumber_pages = [FakePage(tables=[[]])]
        regions = extract_regions.extract_regions(b"fake pdf bytes")
        self.assertEqual([r for r in regions if r["region_type"] == "native_table"], [])

    # Regression test: without this, pdfplumber never releases a page's
    # parsed geometry for the life of the `with pdfplumber.open(...)`
    # block, so peak memory grows with the cumulative complexity of every
    # page processed so far. Measured directly against real documents: a
    # 1345-page PDF exceeded 3.8GB and was killed before finishing without
    # this call; with it, ~103MB start to finish -- not a marginal
    # difference, so this call itself is worth its own regression coverage
    # rather than trusting it stays in place by convention.
    def test_flushes_each_pages_cache_after_extracting_its_tables(self):
        pages = [FakePage(tables=[[["a"]]]), FakePage(tables=[[["b"]]]), FakePage()]
        self.pdfplumber_pages = pages
        extract_regions.extract_regions(b"fake pdf bytes")

        for i, page in enumerate(pages):
            self.assertEqual(page.flush_cache_calls, 1, f"page {i} flush_cache_calls")


class PeakRSSKBTests(unittest.TestCase):
    def test_linux_value_used_as_is(self):
        # ru_maxrss is already KB on Linux, the production runtime.
        with mock.patch.object(extract_regions.sys, "platform", "linux"), mock.patch.object(
            extract_regions.resource, "getrusage"
        ) as getrusage:
            getrusage.return_value = mock.Mock(ru_maxrss=391272)
            self.assertEqual(extract_regions._peak_rss_kb(), 391272)

    def test_macos_value_normalized_from_bytes_to_kb(self):
        # ru_maxrss is bytes on macOS/BSD; without normalizing, a local dev
        # run would misreport peak RSS as 1024x too high.
        with mock.patch.object(extract_regions.sys, "platform", "darwin"), mock.patch.object(
            extract_regions.resource, "getrusage"
        ) as getrusage:
            getrusage.return_value = mock.Mock(ru_maxrss=400_000_000)
            self.assertEqual(extract_regions._peak_rss_kb(), 400_000_000 // 1024)


class IsolatedWorkerTests(ExtractRegionsTestCase):
    """_isolated_worker is the function extract_regions_isolated() runs in
    a spawned child process. Tested here by calling it directly, in-process
    — real multiprocessing plumbing is covered separately below, by
    ExtractRegionsIsolatedTests, since a spawned child re-imports this
    module fresh and would not see mocks set in this test process anyway."""

    def test_success_sends_regions_and_peak_rss(self):
        fake_regions = [{"region_type": "native_text", "page_number": 1}]
        parent_conn, child_conn = multiprocessing.Pipe(duplex=False)
        with mock.patch.object(
            extract_regions, "extract_regions", return_value=fake_regions
        ), mock.patch.object(extract_regions, "_peak_rss_kb", return_value=999), mock.patch.object(
            extract_regions.resource, "setrlimit"
        ) as setrlimit:
            extract_regions._isolated_worker(b"irrelevant", child_conn)

        status, payload = parent_conn.recv()
        self.assertEqual(status, "ok")
        self.assertEqual(payload, {"regions": fake_regions, "peak_rss_kb": 999})
        setrlimit.assert_called_once_with(
            extract_regions.resource.RLIMIT_AS,
            (extract_regions._REGIONS_MEMORY_LIMIT_BYTES, extract_regions._REGIONS_MEMORY_LIMIT_BYTES),
        )

    def test_memory_error_sends_a_clean_error_message(self):
        parent_conn, child_conn = multiprocessing.Pipe(duplex=False)
        with mock.patch.object(
            extract_regions, "extract_regions", side_effect=MemoryError
        ), mock.patch.object(extract_regions.resource, "setrlimit"):
            extract_regions._isolated_worker(b"irrelevant", child_conn)

        status, payload = parent_conn.recv()
        self.assertEqual(status, "error")
        self.assertIn("memory limit", payload)

    def test_other_exception_sends_a_clean_error_message(self):
        parent_conn, child_conn = multiprocessing.Pipe(duplex=False)
        with mock.patch.object(
            extract_regions, "extract_regions", side_effect=ValueError("malformed pdf structure")
        ), mock.patch.object(extract_regions.resource, "setrlimit"):
            extract_regions._isolated_worker(b"irrelevant", child_conn)

        status, payload = parent_conn.recv()
        self.assertEqual(status, "error")
        self.assertEqual(payload, "malformed pdf structure")

    def test_setrlimit_failure_does_not_block_extraction(self):
        # RLIMIT_AS isn't reliably enforced on macOS and can raise there;
        # that must not prevent extraction from proceeding (see
        # _isolated_worker's comment).
        parent_conn, child_conn = multiprocessing.Pipe(duplex=False)
        with mock.patch.object(
            extract_regions, "extract_regions", return_value=[]
        ), mock.patch.object(extract_regions, "_peak_rss_kb", return_value=1), mock.patch.object(
            extract_regions.resource, "setrlimit", side_effect=OSError
        ):
            extract_regions._isolated_worker(b"irrelevant", child_conn)

        status, _payload = parent_conn.recv()
        self.assertEqual(status, "ok")


class ExtractRegionsIsolatedTests(unittest.TestCase):
    """Integration-level: these exercise the real multiprocessing plumbing
    (spawn + pipe + timeout/EOF handling), so — unlike every other test in
    this file — they do NOT mock _load_libs. A freshly spawned child
    process re-imports this module fresh and would not see an in-process
    mock anyway (spawn does not inherit parent-process monkeypatches), so
    real pymupdf/pdfplumber must be installed to run these (they are, in
    scripts/.venv — see requirements.txt)."""

    def test_success_returns_regions_and_the_childs_own_peak_rss(self):
        import pymupdf

        doc = pymupdf.open()
        doc.new_page().insert_text((72, 72), "hello from a real pdf")
        pdf_bytes = doc.tobytes()
        doc.close()

        result = extract_regions.extract_regions_isolated(pdf_bytes)

        text_regions = [r for r in result["regions"] if r["region_type"] == "native_text"]
        self.assertEqual(len(text_regions), 1)
        self.assertIn("hello from a real pdf", text_regions[0]["text"])
        self.assertGreater(result["peak_rss_kb"], 0)

    def test_invalid_pdf_bytes_raise_a_clean_runtime_error_not_a_hang(self):
        with self.assertRaises(RuntimeError):
            extract_regions.extract_regions_isolated(b"this is not a pdf at all")


if __name__ == "__main__":
    unittest.main()
