"""Unit tests for batch_regions_job.py.

Mocks urllib (both the PDF_URL fetch and the RESULT_URL PUT) and
extract_regions' actual extraction so this runs fast without a real PDF
or network access. Run with:
    python3 -m unittest scripts/test_batch_regions_job.py
"""
import contextlib
import io
import json
import unittest
from unittest import mock

import batch_regions_job


class _FakeResponse:
    def __init__(self, body: bytes = b""):
        self._body = body

    def read(self):
        return self._body

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False


class BatchRegionsJobTestCase(unittest.TestCase):
    def setUp(self):
        self.pdf_bytes = b"%PDF-1.4 fake pdf bytes"
        self.put_calls = []

        self._stack = contextlib.ExitStack()
        self.addCleanup(self._stack.close)
        self._stack.enter_context(
            mock.patch.dict(
                "os.environ",
                {"PDF_URL": "https://example.com/doc.pdf", "RESULT_URL": "https://example.com/result.json"},
            )
        )

    def fake_urlopen(self, request_or_url, *args, **kwargs):
        # The GET for PDF_URL is called with a bare URL string; the PUT to
        # RESULT_URL is called with a urllib.request.Request object -- the
        # two call shapes distinguish which leg this is.
        if isinstance(request_or_url, str):
            return _FakeResponse(self.pdf_bytes)
        self.put_calls.append(request_or_url)
        return _FakeResponse()

    def run_main(self):
        stdout = io.StringIO()
        with contextlib.redirect_stdout(stdout):
            batch_regions_job.main()
        return stdout.getvalue()


class SuccessTests(BatchRegionsJobTestCase):
    def test_uploads_regions_from_the_fetched_pdf(self):
        fake_regions = [{"region_type": "native_text", "page_number": 1, "bbox": [0, 0, 1, 1],
                          "text": "hello", "image_base64": "", "needs_vlm": ""}]
        with mock.patch("urllib.request.urlopen", side_effect=self.fake_urlopen), mock.patch(
            "extract_regions.extract_regions", return_value=fake_regions
        ) as extract, mock.patch("extract_regions._peak_rss_kb", return_value=12345):
            output = self.run_main()

        # extract_regions() was called with exactly the bytes fetched from
        # PDF_URL, not a base64-decoded or re-wrapped copy.
        extract.assert_called_once_with(self.pdf_bytes)

        self.assertEqual(len(self.put_calls), 1)
        uploaded = json.loads(self.put_calls[0].data)
        self.assertEqual(uploaded["regions"], fake_regions)
        self.assertEqual(uploaded["peak_rss_kb"], 12345)

        self.assertIn("regions_job: uploaded 1 regions", output)

    def test_result_put_uses_content_type_json(self):
        with mock.patch("urllib.request.urlopen", side_effect=self.fake_urlopen), mock.patch(
            "extract_regions.extract_regions", return_value=[]
        ), mock.patch("extract_regions._peak_rss_kb", return_value=0):
            self.run_main()

        self.assertEqual(self.put_calls[0].get_header("Content-type"), "application/json")
        self.assertEqual(self.put_calls[0].get_method(), "PUT")


class FailureTests(BatchRegionsJobTestCase):
    def test_pdf_fetch_failure_exits_nonzero_and_reports_error(self):
        with mock.patch("urllib.request.urlopen", side_effect=OSError("connection refused")):
            stdout = io.StringIO()
            with contextlib.redirect_stdout(stdout), self.assertRaises(SystemExit) as ctx:
                batch_regions_job.main()

        self.assertEqual(ctx.exception.code, 1)
        output = stdout.getvalue()
        self.assertIn("BATCH_ERROR: fetching PDF_URL", output)
        self.assertEqual(self.put_calls, [])

    def test_extraction_failure_exits_nonzero_and_reports_error(self):
        with mock.patch("urllib.request.urlopen", side_effect=self.fake_urlopen), mock.patch(
            "extract_regions.extract_regions", side_effect=RuntimeError("region extraction exceeded memory limit")
        ):
            stdout = io.StringIO()
            with contextlib.redirect_stdout(stdout), self.assertRaises(SystemExit) as ctx:
                batch_regions_job.main()

        self.assertEqual(ctx.exception.code, 1)
        output = stdout.getvalue()
        self.assertIn("BATCH_ERROR: region extraction failed", output)
        self.assertEqual(self.put_calls, [])

    def test_result_upload_failure_exits_nonzero(self):
        def fake_urlopen_upload_fails(request_or_url, *args, **kwargs):
            if isinstance(request_or_url, str):
                return _FakeResponse(self.pdf_bytes)
            raise OSError("upload failed")

        with mock.patch("urllib.request.urlopen", side_effect=fake_urlopen_upload_fails), mock.patch(
            "extract_regions.extract_regions", return_value=[]
        ), mock.patch("extract_regions._peak_rss_kb", return_value=0):
            stdout = io.StringIO()
            with contextlib.redirect_stdout(stdout), self.assertRaises(SystemExit) as ctx:
                batch_regions_job.main()

        self.assertEqual(ctx.exception.code, 1)
        self.assertIn("BATCH_ERROR: uploading result", stdout.getvalue())


if __name__ == "__main__":
    unittest.main()
