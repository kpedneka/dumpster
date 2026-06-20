import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { LoginPage } from './LoginPage'

const mockSignIn = vi.fn()

vi.mock('@clerk/clerk-react', () => ({
  SignIn: (props: Record<string, unknown>) => {
    mockSignIn(props)
    return <div data-testid="clerk-sign-in" />
  },
}))

describe('LoginPage', () => {
  it('renders Clerk SignIn configured to redirect to /kbs and link to /register', () => {
    render(<LoginPage />)

    expect(screen.getByTestId('clerk-sign-in')).toBeInTheDocument()
    expect(mockSignIn).toHaveBeenCalledWith(
      expect.objectContaining({
        routing: 'virtual',
        signUpUrl: '/register',
        forceRedirectUrl: '/kbs',
      }),
    )
  })
})
