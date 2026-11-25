// SPDX-License-Identifier: AGPL-3.0-only
//
// Log text with colours (logs/logText.ts): the program's ANSI colours in
// the web terminal's palette, and hints from what plain lines say. Pieces
// become text nodes and spans built with h(), never HTML, so every
// character of the log is escaped by Vue; selecting and copying gives the
// log's own text without its escape sequences.
//
// LogText is a block of log (a job log, an error log); its palette follows
// the light and dark mode. LogSpans is one entry of a list (a journal
// message) and costs no more than a span for a plain one; the list sets the
// palette (useAnsiPalette) on an element around the entries.
import { computed, defineComponent, h, type FunctionalComponent, type PropType, type VNode, type VNodeChild } from 'vue'
import { baseColor, styleKey, type AnsiSegment, type AnsiStyle } from '@/logs/ansi'
import { useAnsiPalette } from '@/logs/ansiPalette'
import { logPieces, type LogLine, type LogPiece } from '@/logs/logText'
import { TONE_TEXT, type Tone } from '@/lib/tone'

interface SegmentAttrs {
  class?: string
  style?: string
}

const attrsCache = new Map<string, SegmentAttrs>()

/**
 * A segment's classes and colour. With a background the text takes the
 * colour that reads on it (--ansi-on-N), so no combination of colours can
 * hide text; dim text without a colour is muted.
 */
function segmentAttrs(style: AnsiStyle): SegmentAttrs {
  const key = styleKey(style)
  const known = attrsCache.get(key)
  if (known) return known
  const classes: string[] = []
  if (style.bold) classes.push('font-semibold')
  if (style.italic) classes.push('italic')
  if (style.underline) classes.push('underline')
  let css: string | undefined
  if (style.bg !== undefined) {
    const bg = baseColor(style.bg)
    css = `background-color: var(--ansi-${bg}); color: var(--ansi-on-${bg})`
  } else if (style.fg !== undefined) {
    css = `color: var(--ansi-${baseColor(style.fg)})`
  } else if (style.dim) {
    classes.push('text-muted-foreground')
  }
  const attrs: SegmentAttrs = { class: classes.join(' ') || undefined, style: css }
  if (attrsCache.size > 1024) attrsCache.clear()
  attrsCache.set(key, attrs)
  return attrs
}

function segmentNodes(segments: readonly AnsiSegment[]): VNodeChild[] {
  return segments.map((segment) => (segment.style ? h('span', segmentAttrs(segment.style), segment.text) : segment.text))
}

function lineNode(line: LogLine): VNode {
  const children = segmentNodes(line.body)
  if (line.prefix.length > 0) children.unshift(h('span', { class: 'text-muted-foreground' }, segmentNodes(line.prefix)))
  const tone = line.command ? 'font-semibold text-primary' : line.tone ? TONE_TEXT[line.tone] : undefined
  return h('span', { class: tone }, children)
}

function pieceNodes(pieces: readonly LogPiece[]): VNodeChild[] {
  return pieces.map((piece) => (typeof piece === 'string' ? piece : lineNode(piece)))
}

/** A journal entry's message, or another short log text in a list. */
export const LogSpans: FunctionalComponent<{ text: string; tone?: Tone | null }> = (props) =>
  h('span', { class: props.tone ? TONE_TEXT[props.tone] : undefined }, pieceNodes(logPieces(props.text, !props.tone)))

LogSpans.props = {
  text: { type: String, required: true },
  /** A colour the whole text already has (the entry's priority); lines then get no hints of their own. */
  tone: { type: String as PropType<Tone | null>, default: null },
}

export default defineComponent({
  name: 'LogText',
  props: {
    text: { type: String, required: true },
    /** Long lines wrap instead of scrolling sideways. */
    wrap: { type: Boolean, default: true },
    tag: { type: String, default: 'pre' },
    /** A colour for all of the text; lines then get no hints of their own. */
    tone: { type: String as PropType<Tone | null>, default: null },
  },
  setup(props) {
    const palette = useAnsiPalette()
    const pieces = computed(() => logPieces(props.text, props.tone === null))
    return () =>
      h(
        props.tag,
        {
          class: [props.wrap ? 'whitespace-pre-wrap [overflow-wrap:anywhere]' : 'whitespace-pre', props.tone ? TONE_TEXT[props.tone] : null],
          style: palette.value,
          dir: 'ltr',
        },
        pieceNodes(pieces.value),
      )
  },
})
