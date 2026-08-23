import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { PrivacyPage } from './PrivacyPage'

function renderPrivacyPage() {
  return render(
    <MemoryRouter initialEntries={['/privacy']}>
      <PrivacyPage />
    </MemoryRouter>,
  )
}

describe('PrivacyPage', () => {
  it('states what is collected, and that there is no email or password', () => {
    renderPrivacyPage()
    expect(screen.getByText(/uploaded documents/i)).toBeInTheDocument()
    expect(screen.getByText(/chunks and embeddings/i)).toBeInTheDocument()
    expect(screen.getByText(/anonymous session id/i)).toBeInTheDocument()
    expect(screen.getByText(/no email or password/i)).toBeInTheDocument()
  })

  it('states the exact retention durations from the sweep constants', () => {
    renderPrivacyPage()
    expect(screen.getByText(/2 hours/i)).toBeInTheDocument()
    expect(screen.getByText(/24 hours/i)).toBeInTheDocument()
  })

  it('states that deletion is a hard delete, explicitly contrasted with deactivation', () => {
    renderPrivacyPage()
    expect(screen.getByText(/hard delete, not deactivation/i)).toBeInTheDocument()
  })

  it('lists the third parties data passes through', () => {
    renderPrivacyPage()
    expect(screen.getByText(/openai/i)).toBeInTheDocument()
    expect(screen.getByText(/anthropic/i)).toBeInTheDocument()
    expect(screen.getByText(/cloudflare r2/i)).toBeInTheDocument()
    expect(screen.getByText(/neon/i)).toBeInTheDocument()
  })
})
