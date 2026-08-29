"""Unit tests for extract_entities.py's spaCy pipeline loader.

No pytest/CI harness exists for scripts/ yet (see v4.6) — run directly with:
    python3 -m unittest scripts/test_extract_entities.py
"""
import io
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


if __name__ == "__main__":
    unittest.main()
