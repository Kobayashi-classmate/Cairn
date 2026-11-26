<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// One terminal session in xterm.js, joined over a WebSocket: binary
// messages carry input and output, text messages control ({"type":"resize"}
// from here, {"type":"exit"} from the server). On every (re)connection the
// server first replays recent output, so the screen is cleared first. The
// colours are the account's terminal scheme in the panel's light or dark
// mode, and change with either. Character widths follow Unicode 11, as the
// server's C library counts them (emoji take two cells). The server
// also ends sessions itself, for example when the account's role or
// password changes: the terminal then shows that it ended.
// Loaded on demand, so xterm.js stays out of the main bundle.
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { Unicode11Addon } from '@xterm/addon-unicode11'
import '@xterm/xterm/css/xterm.css'
import { isApiError } from '@/api/client'
import { fetchTermSessions, termSocketUrl, type TermConnection } from '@/api/terminal'
import { useSessionStore } from '@/stores/session'
import { useThemeStore } from '@/stores/theme'
import { schemeTheme } from '@/terminal/schemes'
import { TERMINAL_FONT } from '@/terminal/theme'

const props = defineProps<{ sessionId: string; active: boolean }>()
const emit = defineEmits<{ status: [status: TermConnection] }>()

const { t } = useI18n()
/** Tells screen reader users how to leave the terminal with the keyboard. */
const hintId = `term-hint-${props.sessionId}`
const theme = useThemeStore()
const session = useSessionStore()
/** The terminal's colours now; the margin around it takes the same background. */
const colors = computed(() => schemeTheme(session.terminalScheme, theme.isDark))
const host = ref<HTMLElement | null>(null)
const status = ref<TermConnection>('connecting')

/** Longest pause between reconnection attempts. */
const MAX_DELAY_MS = 15000
/**
 * Connections in a row the server refused although it still lists the
 * session (it closes the socket before it opens when the account may not
 * join it any more): then the session counts as ended.
 */
const MAX_REFUSED = 3
/**
 * A connection counts as working once it stayed open this long; only then
 * do the pauses between attempts start short again (a server that drops a
 * flooding connection right away is not retried every second).
 */
const STABLE_MS = 10000
/** Longest wait for the bundled font before the terminal measures its cells. */
const FONT_WAIT_MS = 1500
const encoder = new TextEncoder()

let term: Terminal | null = null
let fit: FitAddon | null = null
let socket: WebSocket | null = null
let observer: ResizeObserver | null = null
let retryTimer: ReturnType<typeof setTimeout> | undefined
let stableTimer: ReturnType<typeof setTimeout> | undefined
let attempts = 0
let refused = 0
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

async function connect() {
  if (disposed) return
  let url: string
  try {
    // Every connection takes a new one-time ticket.
    url = await termSocketUrl(props.sessionId)
  } catch (err) {
    if (disposed || status.value === 'ended') return
    // Signed out (the sign-in page follows a 401 by itself) or not allowed.
    if (isApiError(err) && (err.status === 401 || err.status === 403)) ended()
    // The panel may be restarting; try again later.
    else scheduleReconnect()
    return
  }
  if (disposed || status.value === 'ended') return
  const ws = new WebSocket(url)
  ws.binaryType = 'arraybuffer'
  socket = ws
  let opened = false
  ws.onopen = () => {
    opened = true
    refused = 0
    clearTimeout(stableTimer)
    stableTimer = setTimeout(() => {
      if (socket === ws) attempts = 0
    }, STABLE_MS)
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
    if (socket === ws) {
      socket = null
      clearTimeout(stableTimer)
    }
    if (disposed || status.value === 'ended') return
    if (!opened) refused++
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
      if (!sessions.some((session) => session.id === props.sessionId) || refused >= MAX_REFUSED) {
        ended()
        return
      }
    } catch (err) {
      // Signed out, or no longer allowed to use terminals: the server has
      // closed the session (the sign-in page follows a 401 by itself).
      if (isApiError(err) && (err.status === 401 || err.status === 403)) {
        ended()
        return
      }
      // The panel may be restarting; try again later.
      scheduleReconnect()
      return
    }
    void connect()
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
    // The Unicode version switch below is still a proposed API.
    allowProposedApi: true,
    cursorBlink: true,
    fontFamily: TERMINAL_FONT,
    fontSize: 14,
    lineHeight: 1.15,
    scrollback: 5000,
    theme: colors.value,
  })
  fit = new FitAddon()
  term.loadAddon(fit)
  // xterm.js measures characters by Unicode 6 unless told otherwise, and so
  // gives emoji one cell where programs count two.
  term.loadAddon(new Unicode11Addon())
  term.unicode.activeVersion = '11'
  term.open(host.value)
  // Tab belongs to the shell, so it cannot leave the terminal. Esc and then
  // Tab within a second moves keyboard focus on instead (as in the code
  // editor); the Escape itself still reaches the shell.
  let escapeAt = -Infinity
  term.attachCustomKeyEventHandler((event) => {
    if (event.type !== 'keydown') return true
    if (event.key === 'Escape') {
      escapeAt = event.timeStamp
      return true
    }
    if (event.key === 'Tab' && !event.ctrlKey && !event.altKey && !event.metaKey && event.timeStamp - escapeAt < 1000) {
      escapeAt = -Infinity
      return false
    }
    return true
  })
  term.textarea?.setAttribute('aria-describedby', hintId)
  term.onData((data) => send(encoder.encode(data)))
  // Some mouse reports are raw bytes, not UTF-8 text.
  term.onBinary((data) => send(Uint8Array.from(data, (char) => char.charCodeAt(0) & 0xff)))
  observer = new ResizeObserver(() => refit())
  observer.observe(host.value)
  refit()
  void connect()
  if (props.active) term.focus()
})

// The mode or the account's scheme changed: the panel's tokens are already
// switched when this runs (theme store), so the Cairn scheme reads the new ones.
watch(colors, (next) => {
  if (term) term.options.theme = next
})

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
  clearTimeout(stableTimer)
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
  <div class="h-full min-h-0 bg-card px-2 pt-2 pb-1" :style="{ backgroundColor: colors.background }">
    <p :id="hintId" class="sr-only">{{ t('terminal.focusHint') }}</p>
    <div ref="host" class="h-full min-h-0" dir="ltr" />
  </div>
</template>
