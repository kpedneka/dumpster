import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import App from './App'

const mockUseAuth = vi.fn()

vi.mock('@/context/AuthContext', () => ({
  useAuth: () => mockUseAuth(),
}))

vi.mock('@/pages/LoginPage', () => ({ LoginPage: () => <div>login page</div> }))
vi.mock('@/pages/RegisterPage', () => ({ RegisterPage: () => <div>register page</div> }))
vi.mock('@/pages/KBsPage', () => ({ KBsPage: () => <div>kbs page</div> }))
vi.mock('@/pages/KBDetailPage', () => ({ KBDetailPage: () => <div>kb detail page</div> }))
vi.mock('@/pages/SearchPage', () => ({ SearchPage: () => <div>search page</div> }))
vi.mock('@/components/AppShell', () => ({ AppShell: () => <div>app shell</div> }))

function renderApp(initialEntry = '/kbs') {
  return render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <App />
    </MemoryRouter>,
  )
}

describe('App / RequireAuth', () => {
  it('renders nothing for a protected route while the session is still loading', () => {
    mockUseAuth.mockReturnValue({ isAuthenticated: false, isLoaded: false })
    const { container } = renderApp('/kbs')
    expect(container).toBeEmptyDOMElement()
  })

  it('redirects to /login for a protected route when not authenticated', () => {
    mockUseAuth.mockReturnValue({ isAuthenticated: false, isLoaded: true })
    renderApp('/kbs')
    expect(screen.getByText('login page')).toBeInTheDocument()
  })

  it('renders the protected route once authenticated', () => {
    mockUseAuth.mockReturnValue({ isAuthenticated: true, isLoaded: true })
    renderApp('/kbs')
    expect(screen.getByText('app shell')).toBeInTheDocument()
  })

  it('renders /login without requiring auth state', () => {
    mockUseAuth.mockReturnValue({ isAuthenticated: false, isLoaded: true })
    renderApp('/login')
    expect(screen.getByText('login page')).toBeInTheDocument()
  })
})
