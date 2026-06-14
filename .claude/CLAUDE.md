## Source of truth

- The **milestone card spec is authoritative** for what to build. The Project Decision Log is historical context for humans and is **not** an instruction — do not act on it unless a decision has been explicitly pasted into your task.
- History is **pull-not-push**: the human may hand you a past decision as context for a specific task. Absent that, build to the current spec.

## Testing (TDD)

- Take a test-driven approach: write tests before or alongside implementation.
- "Done" means tests written, passing, and the **coverage gate is green**. A feature that works but lacks tests is not done.
- Where a card calls for strict TDD, commit failing tests first, then the code that makes them pass, in separate commits.

## Architecture invariants

- **Multi-tenancy:** every domain table carries `user_id`; every query filters on the current tenant. No exceptions.
- **Dependency inversion:** depend on interfaces, not concrete implementations, so units are testable in isolation. No vendor SDK imports outside the dedicated adapter package.
- **Queue/storage/LLM are behind interfaces** — swapping an implementation (e.g. Postgres-queue → Kafka) must be a contained change.

## Code Documentation & Formatting Rules

- Never reference project management milestones such as M0 in code comments. Instead, reference the objective of that milestone.
- Add documentation to all public functions, structs, etc.

## Commits & branches

- One short-lived feature branch per card; merge via PR into `main` after review + green CI.
- Conventional Commits (`feat:`, `fix:`, `test:`, `refactor:`) describing the change.
- **Never put internal milestone codes (e.g. `M4`) in commit messages or PR titles.** They mean nothing to outside readers of the Git history.

## Local workflow

- `make run` (full stack), `make test`, `make lint`, `make migrate`.
- CI runs build + test + lint + coverage gate on every push; a red pipeline blocks merge.