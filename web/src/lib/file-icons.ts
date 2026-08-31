// Maps a file name to its type icon, matching how native OS file browsers
// and most web apps show a type-specific glyph next to a file name rather
// than a generic document icon. Only .txt and .pdf are supported uploads
// (see the KB upload dropzone's accept list), so extension matching is
// sufficient — no content-type lookup needed.
export function fileIconSrc(fileName: string): string {
  return fileName.toLowerCase().endsWith('.pdf') ? '/pdf-icon.svg' : '/txt-icon.svg'
}
