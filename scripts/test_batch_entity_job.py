"""Unit tests for batch_entity_job.py.

Mocks urllib (both the CHUNKS_URL fetch and the RESULT_URL PUT),
extract_entities' pipeline loading, and torch.cuda so this runs fast
without a real GPU or network access. Run with:
    python3 -m unittest scripts/test_batch_entity_job.py
"""
import contextlib
import io
import json
import unittest
from unittest import mock

import batch_entity_job


class _FakeResponse:
    def __init__(self, body: bytes = b""):
        self._body = body

    def read(self):
        return self._body

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False


class BatchEntityJobTestCase(unittest.TestCase):
    def setUp(self):
        self.chunks_body = json.dumps(
            {"allowed_types": ["person"], "chunks": [{"chunk_id": "1", "text": "Ada Lovelace"}]}
        ).encode("utf-8")
        self.fake_nlp = mock.Mock()
        self.fake_model = mock.Mock()
        self.put_calls = []

        self._stack = contextlib.ExitStack()
        self.addCleanup(self._stack.close)
        self._stack.enter_context(
            mock.patch.dict(
                "os.environ",
                {"CHUNKS_URL": "https://example.com/chunks.json", "RESULT_URL": "https://example.com/result.json"},
            )
        )

    def fake_urlopen(self, request_or_url, *args, **kwargs):
        # The GET for CHUNKS_URL is called with a bare URL string; the PUT
        # to RESULT_URL is called with a urllib.request.Request object --
        # the two call shapes distinguish which leg this is.
        if isinstance(request_or_url, str):
            return _FakeResponse(self.chunks_body)
        self.put_calls.append(request_or_url)
        return _FakeResponse()

    def run_main(self):
        stdout = io.StringIO()
        with contextlib.redirect_stdout(stdout):
            batch_entity_job.main()
        return stdout.getvalue()


class SuccessTests(BatchEntityJobTestCase):
    def test_uploads_the_extraction_response_to_result_url(self):
        fake_response = json.dumps(
            {"entities": [{"chunk_id": "1", "type": "person", "text": "Ada Lovelace", "start": 0, "end": 12, "score": 0.9}]}
        )
        with mock.patch("urllib.request.urlopen", side_effect=self.fake_urlopen), mock.patch(
            "extract_entities.load_pipeline", return_value=(self.fake_nlp, self.fake_model)
        ), mock.patch("extract_entities._handle_request", return_value=fake_response) as handle, mock.patch(
            "torch.cuda.is_available", return_value=False
        ):
            output = self.run_main()

        self.assertEqual(len(self.put_calls), 1)
        uploaded = json.loads(self.put_calls[0].data)
        self.assertEqual(uploaded["entities"][0]["text"], "Ada Lovelace")

        # The chunk data fetched from CHUNKS_URL is what got passed
        # through, not something reconstructed independently.
        called_request_json = handle.call_args[0][2]
        self.assertEqual(json.loads(called_request_json), json.loads(self.chunks_body))

        self.assertIn("entity_job: uploaded 1 entities", output)
        self.assertNotIn("BATCH_RESULT:", output)

    def test_result_put_uses_content_type_json(self):
        with mock.patch("urllib.request.urlopen", side_effect=self.fake_urlopen), mock.patch(
            "extract_entities.load_pipeline", return_value=(self.fake_nlp, self.fake_model)
        ), mock.patch("extract_entities._handle_request", return_value=json.dumps({"entities": []})), mock.patch(
            "torch.cuda.is_available", return_value=False
        ):
            self.run_main()

        self.assertEqual(self.put_calls[0].get_header("Content-type"), "application/json")
        self.assertEqual(self.put_calls[0].get_method(), "PUT")

    def test_moves_model_to_cuda_when_available(self):
        cuda_model = mock.Mock()
        cpu_model = mock.Mock()
        cpu_model.to = mock.Mock(return_value=cuda_model)

        with mock.patch("urllib.request.urlopen", side_effect=self.fake_urlopen), mock.patch(
            "extract_entities.load_pipeline", return_value=(self.fake_nlp, cpu_model)
        ), mock.patch("extract_entities._handle_request", return_value=json.dumps({"entities": []})) as handle, mock.patch(
            "torch.cuda.is_available", return_value=True
        ), mock.patch("torch.cuda.get_device_name", return_value="Tesla T4"):
            self.run_main()

        cpu_model.to.assert_called_once_with("cuda")
        self.assertIs(handle.call_args[0][1], cuda_model)

    def test_stays_on_cpu_when_cuda_unavailable(self):
        with mock.patch("urllib.request.urlopen", side_effect=self.fake_urlopen), mock.patch(
            "extract_entities.load_pipeline", return_value=(self.fake_nlp, self.fake_model)
        ), mock.patch("extract_entities._handle_request", return_value=json.dumps({"entities": []})), mock.patch(
            "torch.cuda.is_available", return_value=False
        ):
            output = self.run_main()

        self.fake_model.to.assert_not_called()
        self.assertIn("WARNING", output)


class FailureTests(BatchEntityJobTestCase):
    def test_chunks_url_fetch_failure_exits_nonzero(self):
        with mock.patch("urllib.request.urlopen", side_effect=OSError("connection refused")):
            with self.assertRaises(SystemExit) as ctx:
                self.run_main()
        self.assertEqual(ctx.exception.code, 1)

    def test_chunks_url_fetch_failure_prints_batch_error_and_uploads_nothing(self):
        with mock.patch("urllib.request.urlopen", side_effect=OSError("connection refused")):
            stdout = io.StringIO()
            with contextlib.redirect_stdout(stdout), self.assertRaises(SystemExit):
                batch_entity_job.main()
        output = stdout.getvalue()
        self.assertIn("BATCH_ERROR: fetching CHUNKS_URL", output)
        self.assertEqual(self.put_calls, [])

    def test_extraction_failure_exits_nonzero_and_reports_error(self):
        with mock.patch("urllib.request.urlopen", side_effect=self.fake_urlopen), mock.patch(
            "extract_entities.load_pipeline", return_value=(self.fake_nlp, self.fake_model)
        ), mock.patch("extract_entities._handle_request", side_effect=KeyError("text")), mock.patch(
            "torch.cuda.is_available", return_value=False
        ):
            stdout = io.StringIO()
            with contextlib.redirect_stdout(stdout), self.assertRaises(SystemExit) as ctx:
                batch_entity_job.main()

        self.assertEqual(ctx.exception.code, 1)
        output = stdout.getvalue()
        self.assertIn("BATCH_ERROR: extraction failed", output)
        self.assertEqual(self.put_calls, [])

    def test_result_upload_failure_exits_nonzero(self):
        def fake_urlopen_upload_fails(request_or_url, *args, **kwargs):
            if isinstance(request_or_url, str):
                return _FakeResponse(self.chunks_body)
            raise OSError("upload failed")

        with mock.patch("urllib.request.urlopen", side_effect=fake_urlopen_upload_fails), mock.patch(
            "extract_entities.load_pipeline", return_value=(self.fake_nlp, self.fake_model)
        ), mock.patch("extract_entities._handle_request", return_value=json.dumps({"entities": []})), mock.patch(
            "torch.cuda.is_available", return_value=False
        ):
            stdout = io.StringIO()
            with contextlib.redirect_stdout(stdout), self.assertRaises(SystemExit) as ctx:
                batch_entity_job.main()

        self.assertEqual(ctx.exception.code, 1)
        self.assertIn("BATCH_ERROR: uploading result", stdout.getvalue())


if __name__ == "__main__":
    unittest.main()
