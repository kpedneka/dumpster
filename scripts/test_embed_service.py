"""Unit tests for embed_service.py.

Mocks the model loader so this runs fast without the real
sentence-transformers dependency installed. Run with:
    python3 -m unittest scripts/test_embed_service.py
"""
import unittest
from contextlib import ExitStack
from unittest import mock

from fastapi.testclient import TestClient

import embed_service


class EmbedServiceTestCase(unittest.TestCase):
    """Base class: patches the model loader with a fake and starts a
    TestClient (triggering the lifespan startup) for each test."""

    def setUp(self):
        self.fake_embed_model = mock.Mock()

        self._stack = ExitStack()
        self.addCleanup(self._stack.close)
        self._stack.enter_context(
            mock.patch.object(embed_service.embeddings, "load_embedder", return_value=self.fake_embed_model)
        )
        self.client = self._stack.enter_context(TestClient(embed_service.app))


class HealthzTests(EmbedServiceTestCase):
    def test_ok_once_started(self):
        resp = self.client.get("/healthz")
        self.assertEqual(resp.status_code, 200)
        self.assertEqual(resp.json(), {"status": "ok"})


class EmbeddingsEndpointTests(EmbedServiceTestCase):
    def test_success(self):
        with mock.patch.object(
            embed_service.embeddings, "embed", return_value=[[0.1, 0.2]]
        ) as embed_fn, mock.patch.object(embed_service.embeddings, "dims", return_value=2):
            resp = self.client.post("/embeddings", json={"texts": ["hello"]})

        self.assertEqual(resp.status_code, 200)
        self.assertEqual(resp.json(), {"embeddings": [[0.1, 0.2]], "dims": 2})
        embed_fn.assert_called_once_with(self.fake_embed_model, ["hello"], is_query=False)

    def test_is_query_flag_forwarded(self):
        with mock.patch.object(
            embed_service.embeddings, "embed", return_value=[]
        ) as embed_fn, mock.patch.object(embed_service.embeddings, "dims", return_value=384):
            self.client.post("/embeddings", json={"texts": ["a query"], "is_query": True})

        embed_fn.assert_called_once_with(self.fake_embed_model, ["a query"], is_query=True)

    def test_missing_texts_field_returns_422(self):
        resp = self.client.post("/embeddings", json={})
        self.assertEqual(resp.status_code, 422)


if __name__ == "__main__":
    unittest.main()
