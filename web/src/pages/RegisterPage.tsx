import { SignUp } from '@clerk/clerk-react'

// Clerk's <SignUp> renders the full registration + social-provider
// (Apple / GitHub / Google, as configured in the Clerk dashboard) flow,
// including any verification steps Clerk requires.
export function RegisterPage() {
  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4">
      <SignUp routing="virtual" signInUrl="/login" forceRedirectUrl="/kbs" />
    </div>
  )
}
