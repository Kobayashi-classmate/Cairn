<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// One terminal session in xterm.js, joined over a WebSocket: binary
// messages carry input and output, text messages control ({"type":"resize"}
// from here, {"type":"exit"} from the server). On every (re)connection the
// server first replays recent output, so the screen is cleared first. The
// colours follow the panel's light and dark mode as it changes.
// Loaded on demand, so xterm.js stays out of the main bundle.
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { fetchTermSessions, termSocketUrl, type TermConnection } from '@/api/terminal'
import { useThemeStore } from '@/stores/theme'
import { TERMINAL_FONT, terminalTheme } from '@/terminal/theme'

const props = defineProps<{ sessionId: string; active: boolean }>()
const emit = defineEmits<{ status: [status: TermConnection] }>()

const { t } = useI18n()
const theme = useThemeStore()
const host = ref<HTMLElement | null>(null)
const status = ref<TermConnection>('connecting')

/** Longest pause between reconnection attempts. */
const MAX_DELAY_MS = 15000
/** Longest wait for the bundled font before the terminal measures its cells. */
const FONT_WAIT_MS = 1500
const encoder = new TextEncoder()

let term: Terminal | null = null
let fit: FitAddon | null = null
let socket: WebSocket | null = null
let observer: ResizeObserver | null = null
let retryTimer: ReturnType<typeof setTimeout> | undefined
let attempts = 0
let disposed = false
let sentSize = ''

function setStatus(value: TermConnection) {
  status.value = value
  emit('status', value)
}

function send(data: Uint8Array<ArrayBuffer>) {
  if (socket?.readyState === WebSocket.OPEN) socket.send(data)
}

/** Tells the server the terminal's size when it changed (or always, with `force`). */
function sendSize(force = false) {
  if (!term || socket?.readyState !== WebSocket.OPEN) return
  const cols = Math.min(Math.max(term.cols, 10), 1000)
  const rows = Math.min(Math.max(term.rows, 2), 500)
  const size = `${cols}x${rows}`
  if (!force && size === sentSize) return
  sentSize = size
  socket.send(JSON.stringify({ type: 'resize', cols, rows }))
}

function refit() {
  // A hidden tab has no size; it fits when it is shown.
  if (!fit || !host.value || host.value.offsetWidth === 0) return
  try {
    fit.fit()
  } catch {
    // The renderer is not ready yet; the next resize fits it.
  }
  sendSize()
}

function connect() {
  if (disposed) return
  const ws = new WebSocket(termSocketUrl(props.sessionId))
  ws.binaryType = 'arraybuffer'
  socket = ws
  ws.onopen = () => {
    attempts = 0
    setStatus('open')
    // What follows starts with the replayed output.
    term?.reset()
    refit()
    sendSize(true)
  }
  ws.onmessage = (event: MessageEvent<ArrayBuffer | string>) => {
    if (typeof event.data === 'string') {
      try {
        const control = JSON.parse(event.data) as { type?: string }
        if (control.type === 'exit') ended()
      } catch {
        // Not a control message this client knows.
      }
      return
    }
    term?.write(new Uint8Array(event.data))
  }
  ws.onclose = () => {
    if (socket === ws) socket = null
    if (disposed || status.value === 'ended') return
    scheduleReconnect()
  }
}

function scheduleReconnect() {
  setStatus('reconnecting')
  const delay = Math.min(1000 * 2 ** attempts, MAX_DELAY_MS)
  attempts++
  clearTimeout(retryTimer)
  retryTimer = setTimeout(async () => {
    if (disposed) return
    try {
      // Reconnect only while the session exists; otherwise it has ended.
      const sessions = await fetchTermSessions()
      if (!sessions.some((session) => session.id === props.sessionId)) {
        ended()
        return
      }
    } catch {
      // The panel may be restarting; try again later.
      scheduleReconnect()
      return
    }
    connect()
  }, delay)
}

function ended() {
  clearTimeout(retryTimer)
  setStatus('ended')
  socket?.close()
  term?.write(`\r\n\x1b[2m${t('terminal.ended')}\x1b[0m\r\n`)
}

/** Waits (briefly) for the bundled font, so the cells are measured with it. */
async function fontReady(): Promise<void> {
  try {
    await Promise.race([
      document.fonts.load("14px 'JetBrains Mono Variable'"),
      new Promise((resolve) => setTimeout(resolve, FONT_WAIT_MS)),
    ])
  } catch {
    // The fallback fonts work too.
  }
}

onMounted(async () => {
  await fontReady()
  if (disposed || !host.value) return
  term = new Terminal({
    cursorBlink: true,
    fontFamily: TERMINAL_FONT,
    fontSize: 14,
    lineHeight: 1.15,
    scrollback: 5000,
    theme: terminalTheme(theme.isDark),
  })
  fit = new FitAddon()
  term.loadAddon(fit)
  term.open(host.value)
  term.onData((data) => send(encoder.encode(data)))
  // Some mouse reports are raw bytes, not UTF-8 text.
  term.onBinary((data) => send(Uint8Array.from(data, (char) => char.charCodeAt(0) & 0xff)))
  observer = new ResizeObserver(() => refit())
  observer.observe(host.value)
  refit()
  connect()
  if (props.active) term.focus()
})

// The colour scheme changed: the tokens are already switched when this runs.
watch(
  () => theme.isDark,
  (dark) => {
    if (term) term.options.theme = terminalTheme(dark)
  },
)

watch(
  () => props.active,
  async (active) => {
    if (!active) return
    await nextTick()
    refit()
    term?.focus()
  },
)

onBeforeUnmount(() => {
  disposed = true
  clearTimeout(retryTimer)
  observer?.disconnect()
  socket?.close()
  term?.dispose()
  term = null
})

/**
 * Pastes text as if from the clipboard: line ends become Enter, and a shell
 * that asked for bracketed paste receives it as one block it does not run
 * by itself.
 */
function paste(text: string) {
  if (!term || status.value !== 'open') return
  term.paste(text)
  term.focus()
}

/** Pastes text and presses Enter. */
function run(text: string) {
  if (!term || status.value !== 'open') return
  term.paste(text)
  send(encoder.encode('\r'))
  term.focus()
}

/** The shell asked for bracketed paste (multi-line text then waits for Enter). */
function bracketedPaste(): boolean {
  return term?.modes.bracketedPasteMode ?? false
}

defineExpose({ paste, run, bracketedPaste, focus: () => term?.focus() })
</script>

<template>
  <div class="h-full min-h-0 bg-card px-2 pt-2 pb-1">
    <div ref="host" class="h-full min-h-0" dir="ltr" />
  </div>
</template>
