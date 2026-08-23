// Retention durations below must match internal/account/sweep.go's
// IdleTimeout/HardCap constants exactly — if those change, update this page
// in the same PR rather than letting it silently drift out of date.

export function PrivacyPage() {
  return (
    <div className="rise mx-auto max-w-3xl px-6 py-8">
      <h1 className="mb-2 font-display text-2xl font-semibold tracking-tight">Privacy</h1>
      <p className="mb-8 text-sm text-muted-foreground">
        This is a plain-language description of what Dumpster does with your data, not a legal
        document. Dumpster is a demo: there are no accounts, no email addresses, and no passwords.
      </p>

      <section className="mb-8">
        <h2 className="mb-2 font-display text-lg font-semibold tracking-tight">What's collected</h2>
        <p className="text-sm leading-relaxed text-muted-foreground">
          Uploaded documents, the chunks and embeddings derived from them for search, and an
          anonymous session id used to keep your knowledge bases separate from everyone else's.
          There is no email or password anywhere in the system — a session is identified only by
          a random id stored in a browser cookie.
        </p>
      </section>

      <section className="mb-8">
        <h2 className="mb-2 font-display text-lg font-semibold tracking-tight">How long it's kept</h2>
        <p className="text-sm leading-relaxed text-muted-foreground">
          A session and everything in it is deleted after 2 hours of inactivity, or 24 hours after
          it was created, whichever comes first — regardless of activity.
        </p>
      </section>

      <section className="mb-8">
        <h2 className="mb-2 font-display text-lg font-semibold tracking-tight">
          What deletion actually does
        </h2>
        <p className="text-sm leading-relaxed text-muted-foreground">
          Deletion is a hard delete, not deactivation: uploaded files are removed from object
          storage and the session's database rows are deleted outright. Nothing is kept in a
          disabled or archived state after a session expires.
        </p>
      </section>

      <section>
        <h2 className="mb-2 font-display text-lg font-semibold tracking-tight">
          Third parties data passes through
        </h2>
        <p className="mb-2 text-sm leading-relaxed text-muted-foreground">
          In the course of processing a request, data passes through the following third parties:
        </p>
        <ul className="list-disc space-y-1 pl-5 text-sm leading-relaxed text-muted-foreground">
          <li>OpenAI — generates embeddings for uploaded document text</li>
          <li>Anthropic — generates search answers from retrieved chunks</li>
          <li>Cloudflare R2 — stores uploaded files</li>
          <li>Neon — hosts the Postgres database</li>
        </ul>
      </section>
    </div>
  )
}
