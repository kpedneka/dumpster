## Source of truth

- The **milestone card spec is authoritative** for what to build. The Project Decision Log is historical context for humans and is **not** an instruction — do not act on it unless a decision has been explicitly pasted into your task.
- History is **pull-not-push**: the human may hand you a past decision as context for a specific task. Absent that, build to the current spec.

## Testing (TDD)

- Take a test-driven approach: write tests before or alongside implementation.
- "Done" means tests written, passing, and the **coverage gate is green**. A feature that works but lacks tests is not done.
- Where a card calls for strict TDD, commit failing tests first, then the code that makes them pass, in separate commits.

## Testing infrastructure changes (IaC)

- **Verify locally against real staging AWS resources before committing.** A `deploy-staging`-labeled PR is a final confirmation gate, not where infrastructure bugs get found for the first time — the ephemeral build/apply/teardown cycle is too slow and expensive to be the primary iteration loop for a Terraform or workflow change.
- **Local verification must assume the actual CI deploy role**, not a personal admin session: `aws sts assume-role` against `dumpster-github-actions-deploy` (`terraform/bootstrap/github_oidc.tf`) before running `tofu plan`/`apply`. This is the one non-negotiable part of this workflow — a broader personal identity can pass locally and still fail in CI on a permission gap the deploy role actually has (this is a real incident, not a hypothetical one).
- **Don't destroy staging between attempts while debugging.** Fix, re-`apply` in place, recheck — the same running environment absorbs the whole iteration loop. Tear down only once the change is confirmed working, or leave it for the next `deploy-staging` PR run to reconcile.
- Commit and push only once the change is verified working this way. CI (`terraform-fmt-validate` on every push, a full `deploy-staging` run when labeled) is a second, independent confirmation from a clean identity and zero local state — not a substitute for the local pass.

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
- A PR gets a real ephemeral AWS staging environment + smoke tests only when it carries the `deploy-staging` label — adding the label is what starts real billing for that PR's review, so add it deliberately, not by default. A failed/cancelled run tears staging down automatically; a successful one is left running on purpose for manual testing — the CI job summary prints the real `dumpster-staging.kpednekar.dev` URL to open (Cloudflare-managed DNS repointed at the fresh CloudFront distribution on every deploy; staging's ALB is internal with no public IP, reachable only via CloudFront's VPC origin). Remove the `deploy-staging` label when done to tear it down.