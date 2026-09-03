"""Unit tests for inference_service.py.

Mocks all three model-loading functions so this runs fast without the real
GLiNER/pymupdf/sentence-transformers dependencies installed. Run with:
    python3 -m unittest scripts/test_inference_service.py
"""
import asyncio
import base64
import json
import time
import unittest
from concurrent.futures import ThreadPoolExecutor
from contextlib import ExitStack
from unittest import mock

from fastapi.testclient import TestClient

import inference_service


class InferenceServiceTestCase(unittest.TestCase):
    """Base class: patches all three model loaders with fakes and starts a
    TestClient (triggering the lifespan startup) for each test."""

    def setUp(self):
        self.fake_nlp = mock.Mock()
        self.fake_entity_model = mock.Mock()
        self.fake_embed_model = mock.Mock()

        self._stack = ExitStack()
        self.addCleanup(self._stack.close)
        self._stack.enter_context(
            mock.patch.object(
                inference_service.extract_entities,
                "load_pipeline",
                return_value=(self.fake_nlp, self.fake_entity_model),
            )
        )
        self._stack.enter_context(
            mock.patch.object(inference_service.extract_regions, "_load_libs", return_value=None)
        )
        self._stack.enter_context(
            mock.patch.object(
                inference_service.embeddings, "load_embedder", return_value=self.fake_embed_model
            )
        )
        self.client = self._stack.enter_context(TestClient(inference_service.app))


class HealthzTests(InferenceServiceTestCase):
    def test_ok_once_started(self):
        resp = self.client.get("/healthz")
        self.assertEqual(resp.status_code, 200)
        self.assertEqual(resp.json(), {"status": "ok"})


class EntitiesEndpointTests(InferenceServiceTestCase):
    def test_success_uses_the_pipeline_loaded_at_startup(self):
        fake_response = json.dumps(
            {"entities": [{"chunk_id": "1", "type": "person", "text": "Ada", "start": 0, "end": 3, "score": 0.9}]}
        )
        with mock.patch.object(
            inference_service.extract_entities, "_handle_request", return_value=fake_response
        ) as handle:
            resp = self.client.post(
                "/entities",
                json={"allowed_types": ["person"], "chunks": [{"chunk_id": "1", "text": "Ada Lovelace"}]},
            )

        self.assertEqual(resp.status_code, 200)
        self.assertEqual(resp.json()["entities"][0]["text"], "Ada")
        # Confirms the endpoint reuses the pipeline loaded once at startup,
        # not a fresh one per request.
        called_nlp, called_model = handle.call_args[0][0], handle.call_args[0][1]
        self.assertIs(called_nlp, self.fake_nlp)
        self.assertIs(called_model, self.fake_entity_model)

    def test_malformed_json_returns_400(self):
        resp = self.client.post("/entities", content=b"not json", headers={"Content-Type": "application/json"})
        self.assertEqual(resp.status_code, 400)

    def test_non_object_json_returns_400(self):
        resp = self.client.post("/entities", content=b"[1,2,3]", headers={"Content-Type": "application/json"})
        self.assertEqual(resp.status_code, 400)

    def test_chunk_missing_required_field_returns_400_not_500(self):
        # A chunk missing "text" makes the real _handle_request raise
        # KeyError. This must come back as a 400, not crash the warm
        # process or leak a 500 with an internal stack trace.
        resp = self.client.post(
            "/entities", json={"allowed_types": ["person"], "chunks": [{"chunk_id": "1"}]}
        )
        self.assertEqual(resp.status_code, 400)


class RegionsEndpointTests(InferenceServiceTestCase):
    def test_empty_pdf_base64_returns_empty_regions(self):
        with mock.patch.object(inference_service.extract_regions, "_peak_rss_kb", return_value=123):
            resp = self.client.post("/regions", json={"pdf_base64": ""})
        self.assertEqual(resp.status_code, 200)
        self.assertEqual(resp.json(), {"regions": [], "peak_rss_kb": 123})

    def test_success_decodes_and_extracts(self):
        fake_regions = [{"region_type": "native_text", "page_number": 1}]
        fake_result = {"regions": fake_regions, "peak_rss_kb": 456}
        with mock.patch.object(
            inference_service.extract_regions, "extract_regions_isolated", return_value=fake_result
        ) as extract:
            resp = self.client.post(
                "/regions", json={"pdf_base64": base64.b64encode(b"fake pdf bytes").decode()}
            )

        self.assertEqual(resp.status_code, 200)
        self.assertEqual(resp.json(), fake_result)
        extract.assert_called_once_with(b"fake pdf bytes")

    def test_extraction_failure_returns_500_not_a_crash(self):
        # extract_regions_isolated raises RuntimeError for anything the
        # isolated child couldn't recover from (its own memory limit,
        # a crash, a timeout, or being killed outright) — confirms the
        # endpoint turns that into a clean response instead of a 500 with
        # a leaked stack trace, or worse, an unhandled exception.
        with mock.patch.object(
            inference_service.extract_regions,
            "extract_regions_isolated",
            side_effect=RuntimeError("region extraction exceeded the 1536MB memory limit"),
        ):
            resp = self.client.post(
                "/regions", json={"pdf_base64": base64.b64encode(b"fake pdf bytes").decode()}
            )

        self.assertEqual(resp.status_code, 500)
        self.assertIn("memory limit", resp.json()["error"])

    def test_malformed_json_returns_400(self):
        resp = self.client.post("/regions", content=b"not json", headers={"Content-Type": "application/json"})
        self.assertEqual(resp.status_code, 400)

    def test_invalid_base64_returns_400_not_500(self):
        resp = self.client.post("/regions", json={"pdf_base64": "not-valid-base64!!!"})
        self.assertEqual(resp.status_code, 400)


class EmbeddingsEndpointTests(InferenceServiceTestCase):
    def test_success(self):
        with mock.patch.object(
            inference_service.embeddings, "embed", return_value=[[0.1, 0.2]]
        ) as embed_fn, mock.patch.object(inference_service.embeddings, "dims", return_value=2):
            resp = self.client.post("/embeddings", json={"texts": ["hello"]})

        self.assertEqual(resp.status_code, 200)
        self.assertEqual(resp.json(), {"embeddings": [[0.1, 0.2]], "dims": 2})
        embed_fn.assert_called_once_with(self.fake_embed_model, ["hello"], is_query=False)

    def test_is_query_flag_forwarded(self):
        with mock.patch.object(
            inference_service.embeddings, "embed", return_value=[]
        ) as embed_fn, mock.patch.object(inference_service.embeddings, "dims", return_value=384):
            self.client.post("/embeddings", json={"texts": ["a query"], "is_query": True})

        embed_fn.assert_called_once_with(self.fake_embed_model, ["a query"], is_query=True)

    def test_missing_texts_field_returns_422(self):
        resp = self.client.post("/embeddings", json={})
        self.assertEqual(resp.status_code, 422)


class ConcurrencyTests(InferenceServiceTestCase):
    """Regression tests for the production incident where /regions and
    /entities ran their actual (slow) work directly on the event loop:
    one document's extraction call would silently freeze every other
    in-flight request — including an unrelated /embeddings call for a
    search query — for its entire duration, not just slow it down.

    TestClient's underlying httpx.Client is safe to call from multiple
    threads at once, which is what lets these tests fire two requests that
    genuinely overlap in wall-clock time rather than one waiting for the
    other's synchronous call to complete first."""

    def test_regions_serializes_concurrent_regions_requests_by_default(self):
        # Two large PDFs' /regions calls running at once is what actually
        # OOM-killed this process during local testing — a single one has
        # been measured peaking at ~3.1GB RSS on its own, well past what's
        # safe to double up within this process's memory budget. This is the
        # opposite assertion from before _REGIONS_SEMAPHORE existed: back
        # then, concurrent /regions calls running concurrently (not serially)
        # was the fix for a different incident (see the class docstring).
        # Both are true at once: /regions no longer blocks unrelated
        # endpoints (still tested below), but does now queue behind another
        # /regions call specifically, on purpose.
        def slow_extract(_pdf_bytes):
            time.sleep(0.2)
            return {"regions": [], "peak_rss_kb": 1}

        with mock.patch.object(
            inference_service.extract_regions, "extract_regions_isolated", side_effect=slow_extract
        ):
            body = {"pdf_base64": base64.b64encode(b"fake pdf bytes").decode()}
            started = time.monotonic()
            with ThreadPoolExecutor(max_workers=2) as pool:
                futures = [pool.submit(self.client.post, "/regions", json=body) for _ in range(2)]
                responses = [f.result(timeout=5) for f in futures]
            elapsed = time.monotonic() - started

        for resp in responses:
            self.assertEqual(resp.status_code, 200)
        # Two 0.2s extractions serialized behind the default concurrency-1
        # cap should take close to 0.4s total, not ~0.2s.
        self.assertGreaterEqual(elapsed, 0.35, "requests ran concurrently, not serially")

    def test_regions_concurrency_cap_is_configurable(self):
        # Raising the cap (e.g. once a real per-request memory budget
        # justifies it) should let that many /regions calls actually run at
        # once again, same as before the cap existed.
        def slow_extract(_pdf_bytes):
            time.sleep(0.2)
            return {"regions": [], "peak_rss_kb": 1}

        with mock.patch.object(
            inference_service, "_REGIONS_SEMAPHORE", asyncio.Semaphore(2)
        ), mock.patch.object(
            inference_service.extract_regions, "extract_regions_isolated", side_effect=slow_extract
        ):
            body = {"pdf_base64": base64.b64encode(b"fake pdf bytes").decode()}
            started = time.monotonic()
            with ThreadPoolExecutor(max_workers=2) as pool:
                futures = [pool.submit(self.client.post, "/regions", json=body) for _ in range(2)]
                responses = [f.result(timeout=5) for f in futures]
            elapsed = time.monotonic() - started

        for resp in responses:
            self.assertEqual(resp.status_code, 200)
        self.assertLess(elapsed, 0.35, "requests ran serially despite a cap of 2")

    def test_regions_does_not_block_a_concurrent_healthz_request(self):
        # The more visible symptom in production: an unrelated, cheap
        # request (here /healthz; in production, an /embeddings call for a
        # search query) stalling behind a single slow /regions call.
        def slow_extract(_pdf_bytes):
            time.sleep(0.2)
            return {"regions": [], "peak_rss_kb": 1}

        with mock.patch.object(
            inference_service.extract_regions, "extract_regions_isolated", side_effect=slow_extract
        ):
            body = {"pdf_base64": base64.b64encode(b"fake pdf bytes").decode()}
            with ThreadPoolExecutor(max_workers=2) as pool:
                regions_future = pool.submit(self.client.post, "/regions", json=body)
                # Give the /regions request a head start so it's genuinely
                # in flight before /healthz is sent.
                time.sleep(0.05)
                started = time.monotonic()
                healthz_resp = pool.submit(self.client.get, "/healthz").result(timeout=5)
                healthz_elapsed = time.monotonic() - started
                regions_future.result(timeout=5)

        self.assertEqual(healthz_resp.status_code, 200)
        # /healthz does no work at all — if it took anywhere near as long as
        # the /regions call still in flight, it was blocked behind it.
        self.assertLess(healthz_elapsed, 0.1, "/healthz stalled behind an unrelated /regions call")

    def test_entities_does_not_block_a_concurrent_healthz_request(self):
        def slow_handle_request(_nlp, _model, _raw):
            time.sleep(0.2)
            return json.dumps({"entities": []})

        with mock.patch.object(
            inference_service.extract_entities, "_handle_request", side_effect=slow_handle_request
        ):
            body = {"allowed_types": ["person"], "chunks": [{"chunk_id": "1", "text": "hi"}]}
            with ThreadPoolExecutor(max_workers=2) as pool:
                entities_future = pool.submit(self.client.post, "/entities", json=body)
                time.sleep(0.05)
                started = time.monotonic()
                healthz_resp = pool.submit(self.client.get, "/healthz").result(timeout=5)
                healthz_elapsed = time.monotonic() - started
                entities_future.result(timeout=5)

        self.assertEqual(healthz_resp.status_code, 200)
        self.assertLess(healthz_elapsed, 0.1, "/healthz stalled behind an unrelated /entities call")


if __name__ == "__main__":
    unittest.main()
