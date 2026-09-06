"""Unit tests for extract_entities.py's spaCy pipeline loader and request
handling.

No pytest/CI harness exists for scripts/ yet — run directly with:
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


class ExtractChunkTests(unittest.TestCase):
    """Covers extract_chunk's own filtering, separately from
    _handle_request's chunk_id bookkeeping above."""

    def test_drops_single_character_predictions(self):
        # Real observed noise: GLiNER tagging bare loop-variable names ("i",
        # "s") as "concept" on code-heavy text. See extract_chunk's comment
        # for why a single character can never be a meaningful mention.
        nlp, model = mock.Mock(), mock.Mock()
        sent = mock.Mock(text="i and s are loop counters.", start_char=0)
        nlp.return_value = mock.Mock(sents=[sent])
        model.predict_entities.return_value = [
            {"label": "concept", "text": "i", "start": 0, "end": 1, "score": 0.6},
            {"label": "concept", "text": "s", "start": 6, "end": 7, "score": 0.6},
            {"label": "concept", "text": "loop counters", "start": 12, "end": 25, "score": 0.9},
        ]

        entities = extract_entities.extract_chunk(nlp, model, sent.text, ["concept"])

        self.assertEqual([e["text"] for e in entities], ["loop counters"])

    def test_drops_pronoun_predictions_case_insensitively(self):
        # Real observed noise: "We" tagged as an entity type in one chunk
        # and a different type in another produced two distinct canonical
        # entities that both display as "We" in the same community -- a
        # pronoun is never a legitimate mention under any allowed type,
        # regardless of case or which type GLiNER assigned it.
        nlp, model = mock.Mock(), mock.Mock()
        sent = mock.Mock(text="We analyze the dissection process.", start_char=0)
        nlp.return_value = mock.Mock(sents=[sent])
        model.predict_entities.return_value = [
            {"label": "concept", "text": "We", "start": 0, "end": 2, "score": 0.6},
            {"label": "concept", "text": "dissection", "start": 12, "end": 22, "score": 0.9},
        ]

        entities = extract_entities.extract_chunk(nlp, model, sent.text, ["concept"])

        self.assertEqual([e["text"] for e in entities], ["dissection"])

    def test_drops_indefinite_pronoun_predictions(self):
        # Real observed noise: "someone" surfaced as an entity even after
        # the personal-pronoun-only list was fixed -- indefinite pronouns
        # are a separate closed grammatical class from personal ones.
        nlp, model = mock.Mock(), mock.Mock()
        sent = mock.Mock(text="Someone opened a merge request.", start_char=0)
        nlp.return_value = mock.Mock(sents=[sent])
        model.predict_entities.return_value = [
            {"label": "concept", "text": "Someone", "start": 0, "end": 7, "score": 0.6},
            {"label": "concept", "text": "merge request", "start": 17, "end": 31, "score": 0.9},
        ]

        entities = extract_entities.extract_chunk(nlp, model, sent.text, ["concept"])

        self.assertEqual([e["text"] for e in entities], ["merge request"])

    def test_drops_demonstrative_adverb_predictions(self):
        # Real observed noise: "here" surfaced as an entity -- it's not a
        # pronoun at all, confirming the filter needs to cover function
        # words generally, not just pronouns.
        nlp, model = mock.Mock(), mock.Mock()
        sent = mock.Mock(text="The dissector is defined here.", start_char=0)
        nlp.return_value = mock.Mock(sents=[sent])
        model.predict_entities.return_value = [
            {"label": "concept", "text": "dissector", "start": 4, "end": 13, "score": 0.9},
            {"label": "concept", "text": "here", "start": 27, "end": 31, "score": 0.6},
        ]

        entities = extract_entities.extract_chunk(nlp, model, sent.text, ["concept"])

        self.assertEqual([e["text"] for e in entities], ["dissector"])

    def test_keeps_two_character_predictions(self):
        # The floor is length, not a stopword list -- a real short entity
        # (e.g. an abbreviation) must not be dropped just because it's
        # short.
        nlp, model = mock.Mock(), mock.Mock()
        sent = mock.Mock(text="AI is discussed here.", start_char=0)
        nlp.return_value = mock.Mock(sents=[sent])
        model.predict_entities.return_value = [
            {"label": "concept", "text": "AI", "start": 0, "end": 2, "score": 0.8},
        ]

        entities = extract_entities.extract_chunk(nlp, model, sent.text, ["concept"])

        self.assertEqual([e["text"] for e in entities], ["AI"])


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
