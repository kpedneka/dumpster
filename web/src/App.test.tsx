import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import App from './App'

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

describe('App', () => {
  it('renders the app shell for the kbs route', () => {
    renderApp('/kbs')
    expect(screen.getByText('app shell')).toBeInTheDocument()
  })

  it('redirects the root path to /kbs', () => {
    renderApp('/')
    expect(screen.getByText('app shell')).toBeInTheDocument()
  })

  it('redirects unknown routes to /kbs', () => {
    renderApp('/unknown-route')
    expect(screen.getByText('app shell')).toBeInTheDocument()
  })
})
