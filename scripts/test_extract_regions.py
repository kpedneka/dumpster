"""Unit tests for extract_regions.py.

Mocks pymupdf/pdfplumber (via _load_libs) so this runs fast without those
heavy dependencies installed, same pattern as test_inference_service.py
and test_gpu_entity_service.py. Run with:
    python3 -m unittest scripts/test_extract_regions.py
"""
import unittest
from unittest import mock

import extract_regions


class FakePage:
    def __init__(self, text="", tables=None):
        self._text = text
        self._tables = tables or []

    def get_text(self):
        return self._text

    def extract_tables(self):
        return self._tables


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


if __name__ == "__main__":
    unittest.main()
