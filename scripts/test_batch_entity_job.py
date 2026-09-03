"""Unit tests for batch_entity_job.py.

Mocks urllib (the CHUNKS_URL fetch), extract_entities' pipeline loading,
and torch.cuda so this runs fast without a real GPU or network access. Run
with:
    python3 -m unittest scripts/test_batch_entity_job.py
"""
import contextlib
import io
import json
import unittest
from unittest import mock

import batch_entity_job


class _FakeResponse:
    def __init__(self, body: bytes):
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

        self._stack = contextlib.ExitStack()
        self.addCleanup(self._stack.close)
        self._stack.enter_context(mock.patch.dict("os.environ", {"CHUNKS_URL": "https://example.com/chunks.json"}))
        self._stack.enter_context(
            mock.patch("urllib.request.urlopen", return_value=_FakeResponse(self.chunks_body))
        )

    def run_main(self):
        stdout = io.StringIO()
        with contextlib.redirect_stdout(stdout):
            batch_entity_job.main()
        return stdout.getvalue()


class SuccessTests(BatchEntityJobTestCase):
    def test_result_line_carries_the_extraction_response(self):
        fake_response = json.dumps(
            {"entities": [{"chunk_id": "1", "type": "person", "text": "Ada Lovelace", "start": 0, "end": 12, "score": 0.9}]}
        )
        with mock.patch("extract_entities.load_pipeline", return_value=(self.fake_nlp, self.fake_model)), mock.patch(
            "extract_entities._handle_request", return_value=fake_response
        ) as handle, mock.patch("torch.cuda.is_available", return_value=False):
            output = self.run_main()

        result_lines = [l for l in output.splitlines() if l.startswith("BATCH_RESULT: ")]
        self.assertEqual(len(result_lines), 1)
        payload = json.loads(result_lines[0][len("BATCH_RESULT: "):])
        self.assertEqual(payload["entities"][0]["text"], "Ada Lovelace")

        # The chunk data fetched from CHUNKS_URL is what got passed through,
        # not something reconstructed independently.
        called_request_json = handle.call_args[0][2]
        self.assertEqual(json.loads(called_request_json), json.loads(self.chunks_body))

    def test_moves_model_to_cuda_when_available(self):
        cuda_model = mock.Mock()
        cpu_model = mock.Mock()
        cpu_model.to = mock.Mock(return_value=cuda_model)

        with mock.patch("extract_entities.load_pipeline", return_value=(self.fake_nlp, cpu_model)), mock.patch(
            "extract_entities._handle_request", return_value=json.dumps({"entities": []})
        ) as handle, mock.patch("torch.cuda.is_available", return_value=True), mock.patch(
            "torch.cuda.get_device_name", return_value="Tesla T4"
        ):
            self.run_main()

        cpu_model.to.assert_called_once_with("cuda")
        self.assertIs(handle.call_args[0][1], cuda_model)

    def test_stays_on_cpu_when_cuda_unavailable(self):
        with mock.patch("extract_entities.load_pipeline", return_value=(self.fake_nlp, self.fake_model)), mock.patch(
            "extract_entities._handle_request", return_value=json.dumps({"entities": []})
        ), mock.patch("torch.cuda.is_available", return_value=False):
            output = self.run_main()

        self.fake_model.to.assert_not_called()
        self.assertIn("WARNING", output)


class FailureTests(BatchEntityJobTestCase):
    def test_chunks_url_fetch_failure_exits_nonzero(self):
        with mock.patch("urllib.request.urlopen", side_effect=OSError("connection refused")):
            with self.assertRaises(SystemExit) as ctx:
                self.run_main()
        self.assertEqual(ctx.exception.code, 1)

    def test_chunks_url_fetch_failure_prints_batch_error_not_result(self):
        with mock.patch("urllib.request.urlopen", side_effect=OSError("connection refused")):
            stdout = io.StringIO()
            with contextlib.redirect_stdout(stdout), self.assertRaises(SystemExit):
                batch_entity_job.main()
        output = stdout.getvalue()
        self.assertIn("BATCH_ERROR: fetching CHUNKS_URL", output)
        self.assertNotIn("BATCH_RESULT:", output)

    def test_extraction_failure_exits_nonzero_and_reports_error(self):
        with mock.patch("extract_entities.load_pipeline", return_value=(self.fake_nlp, self.fake_model)), mock.patch(
            "extract_entities._handle_request", side_effect=KeyError("text")
        ), mock.patch("torch.cuda.is_available", return_value=False):
            stdout = io.StringIO()
            with contextlib.redirect_stdout(stdout), self.assertRaises(SystemExit) as ctx:
                batch_entity_job.main()

        self.assertEqual(ctx.exception.code, 1)
        output = stdout.getvalue()
        self.assertIn("BATCH_ERROR: extraction failed", output)
        self.assertNotIn("BATCH_RESULT:", output)


if __name__ == "__main__":
    unittest.main()
