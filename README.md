# dumpster

**[Try it live →](https://dumpster.kpednekar.dev/)**

Upload documents, get a searchable knowledge base with cited, AI-synthesized
answers — powered by hybrid (keyword + vector) search and a GraphRAG-style
entity graph for questions that need to connect facts across documents.

Documents are chunked, embedded, and indexed for both keyword and vector
search as soon as they're uploaded, so search and Q&A are available
immediately. In the background, local entity extraction builds a knowledge
graph — canonicalized entities, co-occurrence edges, and community
detection — that powers multi-hop questions a single-document retrieval
pass can't answer on its own (e.g. "how does X in document A relate to Y in
document B?").

## Architecture

- **Go backend** (`cmd/`, `internal/`): API, background worker, and the
  domain logic behind ingestion, search, and the entity graph. Multi-tenant
  throughout — every domain table and query is scoped to the current user
  or session.
- **Postgres** (via Neon) with pgvector for embeddings and Row-Level
  Security enforcing tenant isolation at the database layer.
- **Local ML inference service** (`scripts/`): a Python/FastAPI service
  wrapping entity extraction, PDF layout classification, and local
  embeddings — kept warm on a separate always-on process so ingestion and
  query paths avoid per-request model load time.
- **Postgres-backed job queue**: ingestion is a multi-stage pipeline
  (region classification → chunking/embedding → entity extraction → edge
  extraction → canonicalization), with each stage a durable, retryable job.
- **Web frontend** (`web/`): the user-facing app.

Queue, object storage, and LLM access are all behind interfaces
(`internal/queue`, `internal/objectstore`, `internal/llm`), so swapping an
implementation is a contained change, not a rewrite.

## Local development

```
make run      # full stack via docker compose
make test     # unit tests + coverage gate
make lint     # go vet/lint + frontend typecheck/lint
make migrate  # apply database migrations
```

See `CLAUDE.md` for the fuller set of engineering conventions this
project follows (testing approach, architecture invariants, commit style).

## License

[GNU AGPL v3](LICENSE). Note the network-use clause: if you run a modified
version of this project as a service others interact with over a network,
the AGPL requires making the complete corresponding source available to
those users.
