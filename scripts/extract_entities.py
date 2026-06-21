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


def load_pipeline():
    """Loads spaCy (for sentence splitting) and GLiNER (for zero-shot
    entity typing). Imported lazily so this module can be imported (e.g. by
    a future test harness) without the heavy ML dependencies installed."""
    import spacy
    from gliner import GLiNER

    try:
        nlp = spacy.load("en_core_web_sm")
    except OSError:
        # Fall back to a blank pipeline with just a sentencizer if the
        # trained model isn't installed; degrades sentence segmentation
        # quality but keeps extraction functional.
        nlp = spacy.blank("en")
        nlp.add_pipe("sentencizer")

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
