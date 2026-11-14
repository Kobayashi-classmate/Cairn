// SPDX-License-Identifier: AGPL-3.0-only
//
// Parser for the small Markdown subset used by official messages and release
// notes: paragraphs, line breaks, **bold**, *italic*, `code`, "- " bullet
// lists and [text](https://...) links. It produces a tree of plain data that
// MarkdownText renders with Vue's h(), so text is always escaped and no HTML
// from the input can reach the page. Anything else is shown as literal text.

export type Inline =
  | { type: 'text'; text: string }
  | { type: 'code'; text: string }
  | { type: 'strong'; children: Inline[] }
  | { type: 'em'; children: Inline[] }
  | { type: 'link'; href: string; children: Inline[] }
  | { type: 'br' }

export type Block =
  | { type: 'paragraph'; children: Inline[] }
  | { type: 'list'; items: Inline[][] }

const LIST_ITEM = /^\s*- (.*)$/
const LINK = /^\[([^\]\n]+)\]\(([^)\s]+)\)/
const MAX_DEPTH = 4

/** Returns the URL when it is an absolute https:// link, otherwise null. */
export function safeHttpsUrl(href: string): string | null {
  try {
    const url = new URL(href)
    return url.protocol === 'https:' ? url.href : null
  } catch {
    return null
  }
}

function pushText(out: Inline[], text: string): void {
  if (text === '') return
  const last = out[out.length - 1]
  if (last?.type === 'text') last.text += text
  else out.push({ type: 'text', text })
}

/** Parses one line of inline markup. Unclosed markers are kept as text. */
function parseInline(text: string, depth = 0, inLink = false): Inline[] {
  const out: Inline[] = []
  let i = 0
  while (i < text.length) {
    const rest = text.slice(i)

    if (rest.startsWith('`')) {
      const end = text.indexOf('`', i + 1)
      if (end > i + 1) {
        out.push({ type: 'code', text: text.slice(i + 1, end) })
        i = end + 1
        continue
      }
    }

    if (depth < MAX_DEPTH && rest.startsWith('**')) {
      const end = text.indexOf('**', i + 2)
      if (end > i + 2) {
        out.push({ type: 'strong', children: parseInline(text.slice(i + 2, end), depth + 1, inLink) })
        i = end + 2
        continue
      }
    }

    if (depth < MAX_DEPTH && rest.startsWith('*') && !rest.startsWith('**')) {
      const end = findSingleStar(text, i + 1)
      if (end > i + 1) {
        out.push({ type: 'em', children: parseInline(text.slice(i + 1, end), depth + 1, inLink) })
        i = end + 1
        continue
      }
    }

    if (!inLink && rest.startsWith('[')) {
      const match = LINK.exec(rest)
      if (match) {
        const [whole, label = '', href = ''] = match
        const url = safeHttpsUrl(href)
        const children = parseInline(label, depth + 1, true)
        if (url) out.push({ type: 'link', href: url, children })
        else out.push(...children) // Not https: keep only the visible text.
        i += whole.length
        continue
      }
    }

    pushText(out, text[i] ?? '')
    i++
  }
  return out
}

/** Index of the next "*" that is not part of "**", or -1. */
function findSingleStar(text: string, from: number): number {
  for (let i = from; i < text.length; i++) {
    if (text[i] !== '*') continue
    if (text[i + 1] === '*') {
      i++
      continue
    }
    return i
  }
  return -1
}

/** Parses the Markdown subset into blocks; never throws. */
export function parseMarkdown(source: string): Block[] {
  const blocks: Block[] = []
  const chunks = source.replace(/\r\n?/g, '\n').split(/\n\s*\n/)
  for (const chunk of chunks) {
    let paragraph: Inline[] | null = null
    let list: Inline[][] | null = null
    for (const line of chunk.split('\n')) {
      if (line.trim() === '') continue
      const item = LIST_ITEM.exec(line)
      if (item) {
        paragraph = null
        if (!list) {
          list = []
          blocks.push({ type: 'list', items: list })
        }
        list.push(parseInline(item[1] ?? ''))
        continue
      }
      list = null
      if (!paragraph) {
        paragraph = []
        blocks.push({ type: 'paragraph', children: paragraph })
      } else {
        paragraph.push({ type: 'br' })
      }
      paragraph.push(...parseInline(line.trim()))
    }
  }
  return blocks
}
