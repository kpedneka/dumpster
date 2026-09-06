#!/usr/bin/env python3
"""Entity extraction logic for the AWS Batch GPU job (see
scripts/batch_entity_job.py, the actual entrypoint, and
internal/entity/awsbatch — the only entity.Extractor implementation).
load_pipeline() and _handle_request() below are what batch_entity_job.py
calls directly, once per job; this file's own main()/stdin loop further
down predates that and is not currently invoked by anything.

Request (one JSON object, batch_entity_job.py's CHUNKS_URL payload):
{
  "allowed_types": ["person", "organization", ...],
  "chunks": [
    {"chunk_id": "<uuid>", "text": "..."},
    ...
  ]
}

Response (one JSON object, returned by _handle_request):
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

Model load is the dominant fixed cost here — measured at ~17.5s regardless
of document size, on top of whatever the extraction itself costs — which is
why batch_entity_job.py loads the pipeline once per job invocation and
reuses it for every chunk in that job's request, rather than reloading it
per chunk.
"""
import json
import sys
import time

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


# _NEVER_ENTITIES is a closed, deliberately narrow set of function words --
# never anything document-dependent like a generic noun ("user", "program"),
# since whether those are noise is context-specific and not ours to decide
# unilaterally. Every word here is a closed-grammatical-class member that
# can never be a legitimate person/organization/location/concept/event/
# work_of_art/date_time mention under any of GLiNER's allowed types
# regardless of document -- a fact about English grammar, not a judgment
# call about this KB's content. Two subcategories, added as each was
# observed in real data:
#   - personal/possessive/reflexive and indefinite pronouns -- "We" tagged
#     as two different types in different chunks produced two distinct
#     canonical entities that both displayed as "We"; "someone" showed up
#     the same way once the personal-pronoun-only version of this list was
#     fixed, since indefinite pronouns are a separate closed class.
#   - demonstrative/locative adverbs -- "here" is not a pronoun at all, so
#     no pronoun list, however complete, was ever going to catch it; this
#     is why the set is function words in general, not "pronouns" specifically.
_NEVER_ENTITIES = frozenset({
    # personal / possessive / reflexive pronouns
    "i", "me", "my", "mine", "myself",
    "we", "us", "our", "ours", "ourselves",
    "you", "your", "yours", "yourself", "yourselves",
    "he", "him", "his", "himself",
    "she", "her", "hers", "herself",
    "it", "its", "itself",
    "they", "them", "their", "theirs", "themselves",
    # indefinite pronouns
    "someone", "anyone", "everyone", "no one", "nobody",
    "something", "anything", "everything", "nothing",
    # demonstrative / locative adverbs
    "here", "there",
})


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
            stripped = p["text"].strip()
            # GLiNER's type set (allowed_types) is closed but not
            # length-aware: on code-heavy text it sometimes tags a bare
            # single-character identifier (e.g. a loop variable "i" or "s")
            # as "concept" with a passing score, since the label is broad
            # enough to be the closest available match for a token that
            # isn't really any of the allowed types. A single character can
            # never be a meaningful person/organization/location/concept
            # mention on its own, so this is a safe, type-agnostic floor
            # rather than a per-type judgment call.
            if len(stripped) < 2:
                continue
            if stripped.lower() in _NEVER_ENTITIES:
                continue
            entities.append({
                "type": p["label"],
                "text": p["text"],
                "start": sent_offset + p["start"],
                "end": sent_offset + p["end"],
                "score": float(p.get("score", 0.0)),
            })
    return entities


def _handle_request(nlp, model, line):
    """Parses one JSON request line and returns the JSON response text
    (without a trailing newline). Isolated from main()'s I/O loop so it's
    testable with fake nlp/model objects, without a real GLiNER model."""
    t_start = time.perf_counter()
    request = json.loads(line)
    allowed_types = request.get("allowed_types", [])
    chunks = request.get("chunks", [])

    out_entities = []
    for c in chunks:
        for e in extract_chunk(nlp, model, c["text"], allowed_types):
            e["chunk_id"] = c["chunk_id"]
            out_entities.append(e)

    print(f"extract_entities: {len(chunks)} chunks -> {len(out_entities)} entities in {time.perf_counter() - t_start:.2f}s")
    return json.dumps({"entities": out_entities})


def main():
    nlp, model = load_pipeline()
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        sys.stdout.write(_handle_request(nlp, model, line) + "\n")
        sys.stdout.flush()


if __name__ == "__main__":
    main()
