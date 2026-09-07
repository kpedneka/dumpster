# Validation corpus

A domain-neutral, fully fictional corpus for evaluating knowledge-graph quality: the intrusion-test coherence metric, the graph-recall metric, and the multi-hop QA eval all run against this same corpus rather than an ad hoc, manually-ingested real document.

## Why fictional

Every person, organization, invention, and place in this corpus is invented. If a multi-hop question used a real fact (e.g. "who founded a real, well-known piece of software"), a judge LLM could answer correctly from its own pretrained knowledge even if the pipeline's retrieval completely failed to surface the relevant chunks — masking exactly the failure these evals exist to catch. Fictional entities force every correct answer to depend on what was actually retrieved.

## Why these seven topics

Six independent storylines (`maritime`, `botany`, `spice_trade`, `architecture`, `music`, `sculpture`), each split across two documents, plus one unrelated control topic (`distractor_sport`). Each storyline was written with a specific, deliberate difficulty:

| Topic | Bridge between its two documents | Tests |
|---|---|---|
| maritime | literal repeated entity name | baseline case — should already work |
| botany | literal repeated name + an in-document abbreviation | abbreviation handling |
| spice_trade | literal name + abbreviation + a same-surname decoy person | abbreviation handling + false-merge guard |
| architecture | one literal bridge + one surname-only reference | **not a clean test** — see below |
| sculpture | surname-only reference, and *nothing* else shared between the two docs | the actual canonicalization partial-name gap |
| music | **no shared entity name at all** | the outer boundary of bridge-entity-based linking — a stretch case, not a near-term target |
| distractor_sport | (control — no intended link to anything) | false cross-topic linking / precision regressions |

**Why architecture needed a sequel.** The original design put a surname-only reference in `architecture` (`architecture_2`) expecting it to fail until canonicalization improves. It doesn't fail — it passes today, for two reasons that have nothing to do with alias resolution: (1) `graphrag`'s seed matching is `LOWER(query) LIKE '%' || normalized_text || '%'`, so seeding from a full name ("Piotr Bellamy") always substring-matches a bare surname canonical entity ("Bellamy") regardless of whether they were ever actually unified into one identity; a short seed can never do the reverse. (2) Co-occurrence is chunk-granular, not sentence-granular — since `architecture`'s two documents both repeatedly mention their shared setting ("Corvenna"), that alone bridges them via a 2-hop traversal, independent of the Bellamy question entirely. `architecture_2` is kept in the fixture (relabeled `easy_via_shared_context`) as an honest example of what a *realistic*, contextually-rich KB actually does — but it can't be used to judge the canonicalization fix. `sculpture_1` is the real test: two nearly bare documents sharing nothing but the tested identity's two textual forms, so neither leak applies.

`ground_truth.json` is the machine-readable fixture: `entities`, `cross_document_pairs` (for the graph-recall metric — does a path exist between two entities that don't co-occur in one chunk), `multi_hop_questions` (for the QA eval — retrieval recall + answer correctness against a known answer), and `canonicalization_test_cases` (positive and negative merge decisions, including two deliberate false-merge traps). Its `design_notes` array spells out both seeding-mechanics lessons above in full, since they constrain how any *future* pair in this fixture must be designed too.

Each `cross_document_pair` and `multi_hop_question` carries a `difficulty`: `easy` (literal bridge, should already be reachable via existing canonicalization + 2-hop traversal), `medium` (in-document abbreviation), `hard` (partial-name reference, needs the canonicalization fix, and is actually isolated enough to prove it), `easy_via_shared_context` (passes today, but not for the reason it looks like it does — not a valid signal either way), or `stretch` (no shared entity at all, needs semantic inference beyond entity identity — documents a ceiling, not a target).

## Using it

Ingest every `.txt` file under this directory into one dedicated benchmark KB through the normal ingestion pipeline, then run the intrusion test, the graph-recall metric, and the multi-hop QA eval against that KB. Re-run the same three after any change to canonicalization, edge derivation, or relation extraction to see what actually moved.
