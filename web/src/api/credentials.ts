// SPDX-License-Identifier: AGPL-3.0-only
//
// The halves of the panel's credentials that are not cookies.
//
// A browser sends a host's cookies to every port of it, so a website on the
// panel's host name receives the panel's cookies too. The server therefore
// never accepts a cookie alone: a session also takes the session key, and a
// known device the device key. Both are told once, in the answer to a
// sign-in, and kept here in web storage, which belongs to the origin (scheme,
// host and port) and is sent nowhere by the browser itself.
//
// - The session key lives in sessionStorage: it survives a reload and ends
//   with the tab. A new tab has none; it asks the tabs already open (same
//   origin only) for theirs, see borrowSessionKey.
// - The device key lives in localStorage, as long as the device cookie.
//
// Neither is ever put in an address, logged or shown.

const SESSION_KEY = 'cairn.session-key'
const DEVICE_KEY = 'cairn.device-key'

/** Used when web storage is unavailable (blocked, or private modes that refuse it): until the next reload. */
const memory = new Map<string, string>()

function read(storage: () => Storage, name: string): string {
  try {
    return storage().getItem(name) ?? memory.get(name) ?? ''
  } catch {
    return memory.get(name) ?? ''
  }
}

function write(storage: () => Storage, name: string, value: string): void {
  if (value) memory.set(name, value)
  else memory.delete(name)
  try {
    if (value) storage().setItem(name, value)
    else storage().removeItem(name)
  } catch {
    // Kept in memory only.
  }
}

const tab = () => window.sessionStorage
const browser = () => window.localStorage

/** The session key of this tab; "" when it has none. */
export function sessionKey(): string {
  return read(tab, SESSION_KEY)
}

export function setSessionKey(key: string): void {
  write(tab, SESSION_KEY, key)
}

export function clearSessionKey(): void {
  write(tab, SESSION_KEY, '')
}

/** The device key of this browser; "" when it has none. */
export function deviceKey(): string {
  return read(browser, DEVICE_KEY)
}

export function setDeviceKey(key: string): void {
  write(browser, DEVICE_KEY, key)
}

// Sharing the session key between tabs. A BroadcastChannel reaches documents
// of this origin only (the port is part of the origin), which are the
// panel's own pages: whatever could listen here could read the storage as
// well. Tabs only answer requests; a tab that signs in or out does not push
// anything to the others.
const CHANNEL = 'cairn.session'

type ChannelMessage = { type: 'want' } | { type: 'key'; key: string }

function isChannelMessage(data: unknown): data is ChannelMessage {
  if (typeof data !== 'object' || data === null) return false
  const message = data as { type?: unknown; key?: unknown }
  return message.type === 'want' || (message.type === 'key' && typeof message.key === 'string')
}

function openChannel(): BroadcastChannel | null {
  try {
    return typeof BroadcastChannel === 'function' ? new BroadcastChannel(CHANNEL) : null
  } catch {
    return null
  }
}

let answering: BroadcastChannel | null = null

/** Lets this tab hand its session key to new tabs of the panel. Call once at startup. */
export function shareSessionKey(): void {
  if (answering) return
  answering = openChannel()
  if (!answering) return
  answering.onmessage = (event: MessageEvent<unknown>) => {
    if (!isChannelMessage(event.data) || event.data.type !== 'want') return
    const key = sessionKey()
    if (key) answering?.postMessage({ type: 'key', key } satisfies ChannelMessage)
  }
}

/**
 * Asks the other tabs of the panel for the session key and keeps the first
 * answer. Resolves to whether a key arrived; a tab alone waits `timeoutMs`.
 * The server decides whether the key is (still) good.
 */
export function borrowSessionKey(timeoutMs = 200): Promise<boolean> {
  return new Promise((resolve) => {
    const channel = openChannel()
    if (!channel) {
      resolve(false)
      return
    }
    const finish = (got: boolean) => {
      clearTimeout(timer)
      channel.close()
      resolve(got)
    }
    const timer = setTimeout(() => finish(false), timeoutMs)
    channel.onmessage = (event: MessageEvent<unknown>) => {
      if (!isChannelMessage(event.data) || event.data.type !== 'key' || !event.data.key) return
      setSessionKey(event.data.key)
      finish(true)
    }
    channel.postMessage({ type: 'want' } satisfies ChannelMessage)
  })
}
