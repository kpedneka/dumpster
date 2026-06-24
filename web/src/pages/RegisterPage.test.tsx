import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { RegisterPage } from './RegisterPage'

const mockSignUp = vi.fn()

vi.mock('@clerk/clerk-react', () => ({
  SignUp: (props: Record<string, unknown>) => {
    mockSignUp(props)
    return <div data-testid="clerk-sign-up" />
  },
}))

describe('RegisterPage', () => {
  it('renders Clerk SignUp configured to redirect to /kbs and link to /login', () => {
    render(<RegisterPage />)

    expect(screen.getByTestId('clerk-sign-up')).toBeInTheDocument()
    expect(mockSignUp).toHaveBeenCalledWith(
      expect.objectContaining({
        routing: 'virtual',
        signInUrl: '/login',
        forceRedirectUrl: '/kbs',
      }),
    )
  })

  it('shows the demo account notice with 7-day deletion warning', () => {
    render(<RegisterPage />)

    expect(screen.getByText(/demo accounts/i)).toBeInTheDocument()
    expect(screen.getByText(/7 days/i)).toBeInTheDocument()
  })
})
