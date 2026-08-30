"""Unit tests for inference_service.py.

Mocks all three model-loading functions so this runs fast without the real
GLiNER/unstructured/sentence-transformers dependencies installed. Run with:
    python3 -m unittest scripts/test_inference_service.py
"""
import base64
import json
import unittest
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
        with mock.patch.object(
            inference_service.extract_regions, "extract_regions", return_value=fake_regions
        ) as extract, mock.patch.object(
            inference_service.extract_regions, "_peak_rss_kb", return_value=456
        ):
            resp = self.client.post(
                "/regions", json={"pdf_base64": base64.b64encode(b"fake pdf bytes").decode()}
            )

        self.assertEqual(resp.status_code, 200)
        self.assertEqual(resp.json(), {"regions": fake_regions, "peak_rss_kb": 456})
        extract.assert_called_once_with(b"fake pdf bytes")

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


if __name__ == "__main__":
    unittest.main()
