"""Unit tests for inference_service.py.

Mocks both model-loading functions so this runs fast without the real
pymupdf/sentence-transformers dependencies installed. Run with:
    python3 -m unittest scripts/test_inference_service.py
"""
import asyncio
import base64
import time
import unittest
from concurrent.futures import ThreadPoolExecutor
from contextlib import ExitStack
from unittest import mock

from fastapi.testclient import TestClient

import inference_service


class InferenceServiceTestCase(unittest.TestCase):
    """Base class: patches both model loaders with fakes and starts a
    TestClient (triggering the lifespan startup) for each test."""

    def setUp(self):
        self.fake_embed_model = mock.Mock()

        self._stack = ExitStack()
        self.addCleanup(self._stack.close)
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
            side_effect=RuntimeError("region extraction exceeded the 1024MB memory limit"),
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
    """Regression tests for the production incident where /regions ran its
    actual (slow) work directly on the event loop: one document's
    extraction call would silently freeze every other in-flight request —
    including an unrelated /embeddings call for a search query — for its
    entire duration, not just slow it down.

    TestClient's underlying httpx.Client is safe to call from multiple
    threads at once, which is what lets these tests fire two requests that
    genuinely overlap in wall-clock time rather than one waiting for the
    other's synchronous call to complete first."""

    def test_regions_allows_concurrency_up_to_the_default_cap(self):
        # The default cap is 2 (real documents now peak at 500-600MB
        # end to end regardless of page count — see extract_regions.py's
        # page.flush_cache() and _REGIONS_MEMORY_LIMIT_BYTES comments — so
        # two isolated, memory-capped children fit comfortably within this
        # process's warm baseline plus the production machine's budget).
        # No explicit semaphore patch here — this exercises the real
        # default, not a configured override (see the two tests below for
        # that).
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
        # Two 0.2s extractions running concurrently under the default cap
        # of 2 should take close to 0.2s total, not ~0.4s.
        self.assertLess(elapsed, 0.35, "requests ran serially despite the default cap of 2")

    def test_regions_serializes_requests_beyond_a_configured_cap(self):
        # A third concurrent request beyond the cap queues, whatever the
        # cap is set to — explicitly patched to 1 here for a simple,
        # unambiguous serialization signal, not because 1 is the default
        # (it isn't; see the test above).
        def slow_extract(_pdf_bytes):
            time.sleep(0.2)
            return {"regions": [], "peak_rss_kb": 1}

        with mock.patch.object(
            inference_service, "_REGIONS_SEMAPHORE", asyncio.Semaphore(1)
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
        # Two 0.2s extractions serialized behind a cap of 1 should take
        # close to 0.4s total, not ~0.2s.
        self.assertGreaterEqual(elapsed, 0.35, "requests ran concurrently despite a cap of 1")

    def test_regions_concurrency_cap_is_configurable_upward(self):
        # Confirms the cap is a real, live override point, not hardcoded —
        # 3 here is deliberately not the default (2), to prove this changes
        # behavior rather than coincidentally matching it.
        def slow_extract(_pdf_bytes):
            time.sleep(0.2)
            return {"regions": [], "peak_rss_kb": 1}

        with mock.patch.object(
            inference_service, "_REGIONS_SEMAPHORE", asyncio.Semaphore(3)
        ), mock.patch.object(
            inference_service.extract_regions, "extract_regions_isolated", side_effect=slow_extract
        ):
            body = {"pdf_base64": base64.b64encode(b"fake pdf bytes").decode()}
            started = time.monotonic()
            with ThreadPoolExecutor(max_workers=3) as pool:
                futures = [pool.submit(self.client.post, "/regions", json=body) for _ in range(3)]
                responses = [f.result(timeout=5) for f in futures]
            elapsed = time.monotonic() - started

        for resp in responses:
            self.assertEqual(resp.status_code, 200)
        self.assertLess(elapsed, 0.35, "requests ran serially despite a cap of 3")

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


if __name__ == "__main__":
    unittest.main()
