"""Unit tests for embeddings.py.

No pytest/CI harness exists for scripts/ yet (see v4.6) — run directly with:
    python3 -m unittest scripts/test_embeddings.py
sentence-transformers is not imported at module level (see load_embedder),
so this runs without the real model installed.
"""
import unittest
from unittest import mock

import embeddings


class EmbedTests(unittest.TestCase):
    def test_empty_texts_returns_empty_no_model_call(self):
        model = mock.Mock()
        self.assertEqual(embeddings.embed(model, []), [])
        model.encode.assert_not_called()

    def test_document_text_not_prefixed(self):
        model = mock.Mock()
        model.encode.return_value = [mock.Mock(tolist=lambda: [0.1, 0.2])]

        embeddings.embed(model, ["a document chunk"], is_query=False)

        model.encode.assert_called_once_with(["a document chunk"], normalize_embeddings=True)

    def test_query_text_gets_bge_instruction_prefix(self):
        # BGE's documented convention: queries need the instruction prefix,
        # passages don't. Skipping this measurably hurts retrieval quality.
        model = mock.Mock()
        model.encode.return_value = [mock.Mock(tolist=lambda: [0.1, 0.2])]

        embeddings.embed(model, ["what is the capital of France?"], is_query=True)

        called_texts = model.encode.call_args[0][0]
        self.assertEqual(len(called_texts), 1)
        self.assertTrue(called_texts[0].startswith("Represent this sentence for searching relevant passages: "))
        self.assertIn("what is the capital of France?", called_texts[0])

    def test_returns_one_vector_per_input_in_order(self):
        model = mock.Mock()
        model.encode.return_value = [
            mock.Mock(tolist=lambda: [1.0, 0.0]),
            mock.Mock(tolist=lambda: [0.0, 1.0]),
        ]

        got = embeddings.embed(model, ["first", "second"])

        self.assertEqual(got, [[1.0, 0.0], [0.0, 1.0]])


class DimsTests(unittest.TestCase):
    def test_dims_available_without_a_loaded_model(self):
        self.assertEqual(embeddings.dims(), 384)


if __name__ == "__main__":
    unittest.main()
