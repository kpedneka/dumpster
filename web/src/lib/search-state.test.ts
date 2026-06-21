import { describe, it, expect, afterEach } from 'vitest'
import { getDraftQuery, setDraftQuery, getSubmittedQuery, setSubmittedQuery, clearSearchState } from './search-state'

afterEach(() => {
  sessionStorage.clear()
})

describe('search-state', () => {
  it('returns an empty string when no draft or submitted query is stored', () => {
    expect(getDraftQuery('kb-1')).toBe('')
    expect(getSubmittedQuery('kb-1')).toBe('')
  })

  it('round-trips a draft query for a given kb', () => {
    setDraftQuery('kb-1', 'capital of france')
    expect(getDraftQuery('kb-1')).toBe('capital of france')
  })

  it('round-trips a submitted query for a given kb', () => {
    setSubmittedQuery('kb-1', 'capital of france')
    expect(getSubmittedQuery('kb-1')).toBe('capital of france')
  })

  it('keeps state isolated between different kbs', () => {
    setDraftQuery('kb-1', 'query for kb 1')
    setDraftQuery('kb-2', 'query for kb 2')
    expect(getDraftQuery('kb-1')).toBe('query for kb 1')
    expect(getDraftQuery('kb-2')).toBe('query for kb 2')
  })

  it('clearSearchState removes all draft and submitted entries across kbs', () => {
    setDraftQuery('kb-1', 'draft 1')
    setSubmittedQuery('kb-1', 'submitted 1')
    setDraftQuery('kb-2', 'draft 2')

    clearSearchState()

    expect(getDraftQuery('kb-1')).toBe('')
    expect(getSubmittedQuery('kb-1')).toBe('')
    expect(getDraftQuery('kb-2')).toBe('')
  })

  it('clearSearchState does not touch unrelated sessionStorage keys', () => {
    sessionStorage.setItem('unrelated-key', 'keep me')
    setDraftQuery('kb-1', 'draft 1')

    clearSearchState()

    expect(sessionStorage.getItem('unrelated-key')).toBe('keep me')
  })
})
