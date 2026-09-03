import { clsx, type ClassValue } from 'clsx'
import { twMerge } from 'tailwind-merge'

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

// Every API error body shares the {error: string} shape (see writeError in
// internal/server) — this extracts that message for a toast/inline display,
// falling back to a generic message for anything else (a network failure, a
// malformed body).
export function describeError(error: unknown, fallback = 'Something went wrong. Try again.'): string {
  if (error && typeof error === 'object' && typeof (error as { error?: unknown }).error === 'string') {
    return (error as { error: string }).error
  }
  return fallback
}
