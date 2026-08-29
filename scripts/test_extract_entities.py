"""Unit tests for extract_entities.py's spaCy pipeline loader and request
handling.

No pytest/CI harness exists for scripts/ yet (see v4.6) — run directly with:
    python3 -m unittest scripts/test_extract_entities.py
"""
import io
import json
import unittest
from unittest import mock

import extract_entities


class LoadNLPTests(unittest.TestCase):
    def test_uses_en_core_web_sm_when_available(self):
        fake_nlp = mock.Mock()
        with mock.patch.object(extract_entities.spacy, "load", return_value=fake_nlp) as load:
            result = extract_entities._load_nlp()
        load.assert_called_once_with("en_core_web_sm")
        self.assertIs(result, fake_nlp)

    def test_falls_back_and_warns_loudly_when_model_missing(self):
        # This is the exact failure mode found in production and local dev:
        # en_core_web_sm was never installed (pip install spacy does not
        # include it), so this fallback silently ran for every document
        # ever processed. The naive sentencizer it falls back to shreds
        # citation/URL/abbreviation-heavy text (e.g. reference lists) into
        # dozens of tiny fragments, each an independent shot at GLiNER
        # finding a spurious "entity" — a 750-token chunk in real data
        # produced 128 entities this way, versus a normal handful. The
        # fallback must now warn loudly so this can't happen invisibly
        # again.
        fake_blank = mock.Mock()
        stderr = io.StringIO()
        with mock.patch.object(extract_entities.spacy, "load", side_effect=OSError("missing")), mock.patch.object(
            extract_entities.spacy, "blank", return_value=fake_blank
        ) as blank, mock.patch("sys.stderr", stderr):
            result = extract_entities._load_nlp()

        blank.assert_called_once_with("en")
        fake_blank.add_pipe.assert_called_once_with("sentencizer")
        self.assertIs(result, fake_blank)
        self.assertIn("en_core_web_sm", stderr.getvalue())
        self.assertIn("spacy download en_core_web_sm", stderr.getvalue())


class HandleRequestTests(unittest.TestCase):
    """_handle_request is the per-line logic main()'s loop delegates to —
    tested in isolation with fake nlp/model objects so it doesn't need the
    real GLiNER model."""

    def test_maps_entities_with_chunk_id(self):
        nlp, model = mock.Mock(), mock.Mock()
        sent = mock.Mock(text="Ada Lovelace wrote notes.", start_char=0)
        nlp.return_value = mock.Mock(sents=[sent])
        model.predict_entities.return_value = [
            {"label": "person", "text": "Ada Lovelace", "start": 0, "end": 12, "score": 0.93}
        ]

        line = json.dumps({
            "allowed_types": ["person"],
            "chunks": [{"chunk_id": "abc-123", "text": "Ada Lovelace wrote notes."}],
        })
        parsed = json.loads(extract_entities._handle_request(nlp, model, line))

        self.assertEqual(len(parsed["entities"]), 1)
        self.assertEqual(parsed["entities"][0]["chunk_id"], "abc-123")
        self.assertEqual(parsed["entities"][0]["type"], "person")

    def test_empty_chunks_returns_empty_entities(self):
        nlp, model = mock.Mock(), mock.Mock()
        line = json.dumps({"allowed_types": [], "chunks": []})
        parsed = json.loads(extract_entities._handle_request(nlp, model, line))
        self.assertEqual(parsed, {"entities": []})


class MainLoopTests(unittest.TestCase):
    def test_processes_multiple_requests_loading_pipeline_once(self):
        # The whole point of this card: the model load must happen once for
        # the process's lifetime, not once per request. Previously
        # extract_entities.py was spawned fresh per document, paying a
        # measured ~17.5s spaCy+GLiNER load cost every single time; this
        # loop is what lets one process serve many documents instead.
        nlp, model = mock.Mock(), mock.Mock()
        nlp.return_value = mock.Mock(sents=[])  # no entities — this test is about looping, not extraction

        lines = [
            json.dumps({"allowed_types": ["person"], "chunks": [{"chunk_id": "1", "text": "a"}]}),
            json.dumps({"allowed_types": ["person"], "chunks": [{"chunk_id": "2", "text": "b"}]}),
        ]
        fake_stdin = io.StringIO("\n".join(lines) + "\n")
        fake_stdout = io.StringIO()

        with mock.patch.object(
            extract_entities, "load_pipeline", return_value=(nlp, model)
        ) as load, mock.patch("sys.stdin", fake_stdin), mock.patch("sys.stdout", fake_stdout):
            extract_entities.main()

        load.assert_called_once()
        out_lines = [line for line in fake_stdout.getvalue().splitlines() if line]
        self.assertEqual(len(out_lines), 2)
        for line in out_lines:
            self.assertEqual(json.loads(line), {"entities": []})

    def test_skips_blank_lines(self):
        nlp, model = mock.Mock(), mock.Mock()
        nlp.return_value = mock.Mock(sents=[])
        line = json.dumps({"allowed_types": ["person"], "chunks": [{"chunk_id": "1", "text": "a"}]})
        fake_stdin = io.StringIO(f"\n{line}\n\n")
        fake_stdout = io.StringIO()

        with mock.patch.object(
            extract_entities, "load_pipeline", return_value=(nlp, model)
        ), mock.patch("sys.stdin", fake_stdin), mock.patch("sys.stdout", fake_stdout):
            extract_entities.main()

        out_lines = [line for line in fake_stdout.getvalue().splitlines() if line]
        self.assertEqual(len(out_lines), 1)


if __name__ == "__main__":
    unittest.main()
