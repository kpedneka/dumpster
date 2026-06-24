import { SignUp } from '@clerk/clerk-react'

// Clerk's <SignUp> renders the full registration + social-provider
// (Apple / GitHub / Google, as configured in the Clerk dashboard) flow,
// including any verification steps Clerk requires.
export function RegisterPage() {
  return (
    <div className="rise flex min-h-screen flex-col items-center justify-center gap-6 bg-background px-4 py-10">
      <div className="flex flex-col items-center gap-2 text-center">
        <div className="flex items-center gap-2">
          <span className="grid h-8 w-8 place-items-center rounded-lg bg-primary font-display text-lg font-semibold text-primary-foreground">
            D
          </span>
          <span className="font-display text-xl font-semibold tracking-tight">Dumpster</span>
        </div>
        <p className="text-sm text-muted-foreground">Multimodal knowledge base</p>
      </div>

      <SignUp routing="virtual" signInUrl="/login" forceRedirectUrl="/kbs" />

      {/* v2.8: demo-account expiry notice */}
      <p className="max-w-sm text-center text-xs leading-relaxed text-muted-foreground">
        Accounts created here are <strong className="font-medium text-foreground">demo accounts</strong>. Your
        account and all of its data are automatically and permanently deleted 7 days after sign-up.
      </p>
    </div>
  )
}
