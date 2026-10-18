// Retention durations below must match internal/account/sweep.go's
// IdleTimeout/HardCap constants exactly — if those change, update this page
// in the same PR rather than letting it silently drift out of date.

// Consumer-facing policy pages, not the service terms that technically
// govern each provider's handling of this app's data — linked here as the
// practical, readable reference, consistent with this page's plain-language
// framing. Bedrock links to its data-protection page instead of the general
// AWS policy because that's where AWS states prompts aren't shared with
// model providers, which is the question a reader of this list actually has.
const THIRD_PARTIES = [
  {
    name: 'Amazon Web Services',
    href: 'https://aws.amazon.com/privacy/',
    role:
      'hosts the app; S3 stores uploaded files, which are scanned for malware before processing; ' +
      'embeddings are computed on AWS servers this app runs, with an open-source model, ' +
      'rather than sent to an outside embeddings provider',
  },
  {
    name: 'Amazon Bedrock',
    href: 'https://docs.aws.amazon.com/bedrock/latest/userguide/data-protection.html',
    role:
      "generates search answers from retrieved chunks using Anthropic's Claude models, run " +
      'inside AWS — prompts and answers are not shared with Anthropic',
  },
  {
    name: 'Neon',
    href: 'https://neon.com/privacy-policy',
    role: 'hosts the Postgres database',
  },
] as const

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
        <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
          Your IP address is used for rate limiting, in a short-lived counter that is cleared
          automatically, and appears in the web firewall's request logs, which are kept for 30
          days to investigate abuse.
        </p>
      </section>

      <section className="mb-8">
        <h2 className="mb-2 font-display text-lg font-semibold tracking-tight">How long it's kept</h2>
        <p className="text-sm leading-relaxed text-muted-foreground">
          A session and everything in it is deleted after 6 hours of inactivity, or 24 hours after
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
        <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
          Deleted database rows remain recoverable from the database provider's restore history for
          up to 6 hours, which exists for disaster recovery only, before they are permanently gone.
          No other database backups are kept.
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
          {THIRD_PARTIES.map(({ name, href, role }) => (
            <li key={name}>
              <a
                href={href}
                target="_blank"
                rel="noopener noreferrer"
                className="underline hover:text-foreground"
              >
                {name}
              </a>{' '}
              — {role}
            </li>
          ))}
        </ul>
      </section>
    </div>
  )
}
