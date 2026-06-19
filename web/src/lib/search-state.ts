const DRAFT_PREFIX = 'search-draft:'
const SUBMITTED_PREFIX = 'search-submitted:'

export function getDraftQuery(kbId: string): string {
  return sessionStorage.getItem(DRAFT_PREFIX + kbId) ?? ''
}

export function setDraftQuery(kbId: string, value: string): void {
  sessionStorage.setItem(DRAFT_PREFIX + kbId, value)
}

export function getSubmittedQuery(kbId: string): string {
  return sessionStorage.getItem(SUBMITTED_PREFIX + kbId) ?? ''
}

export function setSubmittedQuery(kbId: string, value: string): void {
  sessionStorage.setItem(SUBMITTED_PREFIX + kbId, value)
}

// Called on logout so the next signed-in user on this tab never sees a
// previous tenant's search drafts or queries.
export function clearSearchState(): void {
  for (let i = sessionStorage.length - 1; i >= 0; i--) {
    const key = sessionStorage.key(i)
    if (key && (key.startsWith(DRAFT_PREFIX) || key.startsWith(SUBMITTED_PREFIX))) {
      sessionStorage.removeItem(key)
    }
  }
}
