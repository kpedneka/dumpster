import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { AuthProvider, useAuth } from './AuthContext'

function Probe() {
  const { isAuthenticated } = useAuth()
  return <span data-testid="authenticated">{String(isAuthenticated)}</span>
}

describe('AuthProvider', () => {
  it('reports the session as always authenticated', () => {
    render(
      <AuthProvider>
        <Probe />
      </AuthProvider>,
    )
    expect(screen.getByTestId('authenticated')).toHaveTextContent('true')
  })

  it('throws when useAuth is called outside AuthProvider', () => {
    function Orphan() {
      useAuth()
      return null
    }
    expect(() => render(<Orphan />)).toThrow('useAuth must be used inside AuthProvider')
  })
})
