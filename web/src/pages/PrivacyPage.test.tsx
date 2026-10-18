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
    expect(screen.getByText(/deleted after 6 hours of inactivity/i)).toBeInTheDocument()
    expect(screen.getByText(/24 hours/i)).toBeInTheDocument()
  })

  it("discloses how long deleted rows stay recoverable in the database provider's history", () => {
    renderPrivacyPage()
    expect(
      screen.getByText(/recoverable from the database provider's restore history for up to 6 hours/i),
    ).toBeInTheDocument()
  })

  it('states that deletion is a hard delete, explicitly contrasted with deactivation', () => {
    renderPrivacyPage()
    expect(screen.getByText(/hard delete, not deactivation/i)).toBeInTheDocument()
  })

  it('states that IP addresses are used only for rate limiting and firewall logs', () => {
    renderPrivacyPage()
    expect(screen.getByText(/ip address/i)).toBeInTheDocument()
    expect(screen.getByText(/rate limiting/i)).toBeInTheDocument()
    expect(screen.getByText(/30 days/i)).toBeInTheDocument()
  })

  it('lists the third parties data passes through', () => {
    renderPrivacyPage()
    expect(screen.getByText(/s3 stores uploaded files/i)).toBeInTheDocument()
    expect(screen.getByText(/open-source model/i)).toBeInTheDocument()
    expect(screen.getByText(/anthropic's claude models/i)).toBeInTheDocument()
    expect(screen.getByText(/hosts the postgres database/i)).toBeInTheDocument()
  })

  it('no longer lists providers the app has stopped sending data to', () => {
    renderPrivacyPage()
    expect(screen.queryByText(/openai/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/cloudflare/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/\br2\b/i)).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Anthropic' })).not.toBeInTheDocument()
  })

  it('links each third party to its own privacy policy, opened in a new tab', () => {
    renderPrivacyPage()

    const expectations: [name: string, href: string][] = [
      ['Amazon Web Services', 'https://aws.amazon.com/privacy/'],
      ['Amazon Bedrock', 'https://docs.aws.amazon.com/bedrock/latest/userguide/data-protection.html'],
      ['Neon', 'https://neon.com/privacy-policy'],
    ]

    for (const [name, href] of expectations) {
      const link = screen.getByRole('link', { name })
      expect(link).toHaveAttribute('href', href)
      expect(link).toHaveAttribute('target', '_blank')
      expect(link).toHaveAttribute('rel', expect.stringContaining('noopener'))
    }
  })
})
