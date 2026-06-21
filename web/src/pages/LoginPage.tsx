import { SignIn } from '@clerk/clerk-react'

// Clerk's <SignIn> renders the full credential + social-provider (Apple /
// GitHub / Google, as configured in the Clerk dashboard) sign-in flow.
// "virtual" routing keeps Clerk's internal steps (e.g. email code
// verification) within this component instead of requiring a nested
// catch-all React Router route.
export function LoginPage() {
  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4">
      <SignIn routing="virtual" signUpUrl="/register" forceRedirectUrl="/kbs" />
    </div>
  )
}
