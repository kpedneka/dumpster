import { Popover, PopoverTrigger, PopoverContent } from '@/components/ui/popover'
import type { components } from '@/api/schema.d.ts'

type Citation = components['schemas']['Citation']

interface CitationGroupProps {
  // One or more citations the model cited immediately adjacent to each
  // other (e.g. "[1][2]") — see the prompt instruction in answerer.go.
  // Grouping and file-level dedup happens before this component ever sees
  // the citations; it only renders what it's given.
  citations: Citation[]
}

interface FileGroup {
  fileName: string
  documentId: string
  pages: number[]
}

// Groups citations by document, mirroring KBDetailPage's citedPagesFor: two
// citations from the same file collapse into one entry with every distinct
// page they named, sorted ascending — a "+k" badge counts distinct files,
// not raw citation count, so two pages of the same PDF is never "+1".
function groupByFile(citations: Citation[]): FileGroup[] {
  const byDoc = new Map<string, FileGroup>()
  for (const c of citations) {
    let group = byDoc.get(c.document_id)
    if (!group) {
      group = { fileName: c.file_name, documentId: c.document_id, pages: [] }
      byDoc.set(c.document_id, group)
    }
    if (c.locator?.type === 'page' && !group.pages.includes(c.locator.value)) {
      group.pages.push(c.locator.value)
    }
  }
  for (const group of byDoc.values()) {
    group.pages.sort((a, b) => a - b)
  }
  return Array.from(byDoc.values())
}

// The inline marker shows the file name it came from — borrowed from how
// Google shows a source's site name rather than a bare footnote number —
// plus a "+k" for any additional distinct files behind the same claim,
// rather than stacking one "[N]" badge per citation. The popover lists
// every file in the group with its page(s), no chunk-text quote: the file
// (+ page, when one applies) is the entire citation identity now, matching
// the file-level "relevant files" ranking shown elsewhere on the page.
export function CitationMarker({ citations }: CitationGroupProps) {
  const groups = groupByFile(citations)
  const [primary, ...rest] = groups

  return (
    <Popover>
      <PopoverTrigger asChild>
        <sup
          tabIndex={0}
          onKeyDown={(e) => {
            if (e.key === 'Enter' || e.key === ' ') {
              e.preventDefault()
              e.currentTarget.click()
            }
          }}
          className="ml-0.5 cursor-pointer rounded bg-accent px-1 py-0.5 text-xs font-medium text-accent-foreground hover:bg-accent/80"
        >
          {primary.fileName}
          {rest.length > 0 && ` +${rest.length}`}
        </sup>
      </PopoverTrigger>
      <PopoverContent className="max-h-[min(24rem,70vh)] overflow-y-auto">
        <ul className="space-y-2">
          {groups.map((group) => {
            const locatorLabel = group.pages.length > 0 ? `p. ${group.pages.join(', ')}` : null
            return (
              <li key={group.documentId} className="text-sm">
                <span className="font-medium">{group.fileName}</span>
                {locatorLabel && <span className="text-muted-foreground">{` · ${locatorLabel}`}</span>}
              </li>
            )
          })}
        </ul>
      </PopoverContent>
    </Popover>
  )
}
