// SPDX-License-Identifier: AGPL-3.0-only

/**
 * Starts downloading from an address the server just handed out (it
 * answers with the file, so the page stays). `filename` suggests a name for
 * the panel's own downloads; other sites' (a bucket's link) name their own.
 */
export function startDownload(url: string, filename?: string): void {
  const link = document.createElement('a')
  link.href = url
  if (filename) link.download = filename
  document.body.append(link)
  link.click()
  link.remove()
}

/** Saves text as a file through a temporary object URL; nothing leaves the browser. */
export function downloadText(filename: string, text: string, type = 'text/plain'): void {
  const blob = new Blob([text], { type: `${type};charset=utf-8` })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  link.click()
  // Give the browser a moment to start the download before releasing the URL.
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
