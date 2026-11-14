// SPDX-License-Identifier: AGPL-3.0-only
import { createI18n } from 'vue-i18n'

/** A locale file: nested objects whose leaves are vue-i18n messages. */
export interface MessageTree {
  [key: string]: string | MessageTree
}

interface LocaleMeta {
  /** The language's name written in that language, shown in the language switcher. */
  nativeName: string
}

/** English is the source language and the fallback for every missing message. */
export const SOURCE_LOCALE: string = 'en'

/**
 * Locale files live at `src/locales/<locale>/<area>.json`. Each area file
 * becomes the top-level namespace of its keys, so `errors.json` provides
 * `errors.*`. `meta.json` describes the locale itself and holds no messages.
 */
const LOCALE_FILE = /\/locales\/([^/]+)\/([^/]+)\.json$/

function parseLocalePath(path: string): { locale: string; area: string } | null {
  const match = LOCALE_FILE.exec(path)
  const locale = match?.[1]
  const area = match?.[2]
  return locale && area ? { locale, area } : null
}

// One tiny file per locale, bundled eagerly so the switcher can list all languages.
const metaFiles = import.meta.glob<LocaleMeta>('../locales/*/meta.json', {
  eager: true,
  import: 'default',
})

// The source locale is bundled eagerly so fallback messages are always available.
const sourceFiles = import.meta.glob<MessageTree>(
  ['../locales/en/*.json', '!../locales/en/meta.json'],
  { eager: true, import: 'default' },
)

// Every other locale is split into separate chunks and fetched when selected.
const lazyFiles = import.meta.glob<MessageTree>([
  '../locales/*/*.json',
  '!../locales/en/*.json',
  '!../locales/*/meta.json',
])

const nativeNames = new Map<string, string>()
for (const [path, meta] of Object.entries(metaFiles)) {
  const parsed = parseLocalePath(path)
  if (parsed) nativeNames.set(parsed.locale, meta.nativeName)
}

/** Locales that ship with this build, source locale first. */
export const bundledLocales: readonly string[] = [...nativeNames.keys()].sort((a, b) =>
  a === SOURCE_LOCALE ? -1 : b === SOURCE_LOCALE ? 1 : a.localeCompare(b),
)

export function localeNativeName(locale: string): string {
  return nativeNames.get(locale) ?? locale
}

function sourceMessages(): MessageTree {
  const tree: MessageTree = {}
  for (const [path, messages] of Object.entries(sourceFiles)) {
    const parsed = parseLocalePath(path)
    if (parsed) tree[parsed.area] = messages
  }
  return tree
}

export const i18n = createI18n({
  legacy: false,
  locale: SOURCE_LOCALE,
  fallbackLocale: SOURCE_LOCALE,
  messages: { [SOURCE_LOCALE]: sourceMessages() } as Record<string, MessageTree>,
  // Falling back to English for an untranslated message is expected behaviour.
  fallbackWarn: false,
  missingWarn: import.meta.env.DEV,
})

const loads = new Map<string, Promise<void>>()

/** Fetches and registers every area file of a locale; repeated calls share one load. */
export function loadLocaleMessages(locale: string): Promise<void> {
  if (locale === SOURCE_LOCALE) return Promise.resolve()
  const pending = loads.get(locale)
  if (pending) return pending

  const files = Object.entries(lazyFiles).flatMap(([path, load]) => {
    const parsed = parseLocalePath(path)
    return parsed?.locale === locale ? [{ area: parsed.area, load }] : []
  })
  if (files.length === 0) {
    return Promise.reject(new Error(`locale "${locale}" is not bundled`))
  }

  const load = Promise.all(files.map(async (file) => [file.area, await file.load()] as const)).then(
    (areas) => {
      i18n.global.setLocaleMessage(locale, Object.fromEntries(areas))
    },
  )
  loads.set(locale, load)
  // Allow a retry after a failed download.
  load.catch(() => loads.delete(locale))
  return load
}

/** The locale currently used to render the UI. */
export function currentLocale(): string {
  return i18n.global.locale.value
}

/** Loads a locale if needed and switches the whole UI to it without a reload. */
export async function activateLocale(locale: string): Promise<void> {
  await loadLocaleMessages(locale)
  i18n.global.locale.value = locale
  document.documentElement.lang = locale
}

/**
 * Picks the text for the current UI language from per-locale content sent by
 * the server: the current locale, then English, then any non-empty entry.
 */
export function pickLocalized(texts: Record<string, string> | null | undefined): string {
  if (!texts) return ''
  return texts[currentLocale()] || texts[SOURCE_LOCALE] || Object.values(texts).find(Boolean) || ''
}
