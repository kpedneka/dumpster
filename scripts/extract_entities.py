#!/usr/bin/env python3
"""Local entity extraction sidecar for the dumpster ingestion pipeline.

Run once per invocation by the Go gliner adapter (internal/entity/gliner)
via subprocess: JSON request on stdin, JSON response on stdout. This keeps
the extraction model (spaCy for sentence/token structure, GLiNER for
zero-shot entity typing against a config-driven label set) local, turning a
variable per-document LLM cost into a fixed, sunk hardware cost.

Request (stdin), one JSON object:
{
  "allowed_types": ["person", "organization", ...],
  "chunks": [
    {"chunk_id": "<uuid>", "text": "..."},
    ...
  ]
}

Response (stdout), one JSON object:
{
  "entities": [
    {
      "chunk_id": "<uuid>",
      "type": "person",
      "text": "Ada Lovelace",
      "start": 0,
      "end": 12,
      "score": 0.93
    },
    ...
  ]
}

GLiNER (https://github.com/urchade/GLiNER) is a zero-shot NER model: it
accepts an arbitrary label set at inference time rather than being trained
against a fixed schema, which is what lets the entity type list live in
config (ENTITY_TYPES) instead of requiring a retrained/fine-tuned model per
type-set change. spaCy is used for fast sentence segmentation so each GLiNER
call stays within the model's effective context window.

Models are loaded lazily and once per process so repeated invocations in a
warm sidecar (rather than a fresh process per document) amortize load time;
the current adapter still invokes this script once per document, which is
the simplest viable wiring per the architecture notes, with sidecar/warm-
process optimization left as a follow-up if throughput becomes a concern.
"""
import json
import sys

import spacy


def _load_nlp():
    """Loads the spaCy pipeline used for sentence segmentation, falling back
    to a bare sentencizer if en_core_web_sm isn't installed.

    That fallback found silent for months in both local dev and the
    production Docker image (neither ever ran `python -m spacy download
    en_core_web_sm` — `pip install spacy` only installs the library, not the
    model). Its naive punctuation-based splitting badly fragments
    citation/URL/abbreviation-heavy text: a real 750-token reference-list
    chunk was shredded into 56 fragments (avg. 6.6 words, e.g. "USDA. (",
    "2019, August 20).") instead of ~15 real sentences, and GLiNER predicting
    independently on each fragment inflated that one chunk to 128 "entities"
    versus a normal handful — which in turn produced a combinatorial
    explosion of co-occurrence edges downstream. Warn loudly so a missing
    model is never silently corrupting extraction quality again.
    """
    try:
        return spacy.load("en_core_web_sm")
    except OSError:
        print(
            "WARNING: en_core_web_sm not installed — falling back to naive "
            "punctuation-based sentence splitting, which badly fragments "
            "citation/URL/abbreviation-heavy text and inflates entity and "
            "co-occurrence-edge counts. Fix with: "
            "python -m spacy download en_core_web_sm",
            file=sys.stderr,
        )
        nlp = spacy.blank("en")
        nlp.add_pipe("sentencizer")
        return nlp


def load_pipeline():
    """Loads spaCy (for sentence splitting) and GLiNER (for zero-shot
    entity typing). GLiNER is imported lazily so this module can be imported
    (e.g. by a test harness) without its heavier ML dependencies installed."""
    from gliner import GLiNER

    nlp = _load_nlp()
    model = GLiNER.from_pretrained("urchade/gliner_mediumv2.1")
    return nlp, model


def extract_chunk(nlp, model, text, allowed_types, threshold=0.5):
    """Returns a list of entity dicts for one chunk's text, with start/end
    byte offsets relative to the start of text."""
    entities = []
    doc = nlp(text)
    for sent in doc.sents:
        sent_text = sent.text
        sent_offset = sent.start_char
        preds = model.predict_entities(sent_text, allowed_types, threshold=threshold)
        for p in preds:
            entities.append({
                "type": p["label"],
                "text": p["text"],
                "start": sent_offset + p["start"],
                "end": sent_offset + p["end"],
                "score": float(p.get("score", 0.0)),
            })
    return entities


def main():
    request = json.load(sys.stdin)
    allowed_types = request.get("allowed_types", [])
    chunks = request.get("chunks", [])

    out_entities = []
    if chunks and allowed_types:
        nlp, model = load_pipeline()
        for c in chunks:
            for e in extract_chunk(nlp, model, c["text"], allowed_types):
                e["chunk_id"] = c["chunk_id"]
                out_entities.append(e)

    json.dump({"entities": out_entities}, sys.stdout)


if __name__ == "__main__":
    main()
