// SPDX-License-Identifier: AGPL-3.0-only

interface LocaleKey {
  tag: string
  language: string
  script: string
}

function localeKey(tag: string): LocaleKey | null {
  try {
    // maximize() fills in the likely script, so "zh" and "zh-SG" become
    // zh-Hans (matching zh-CN) while "zh-TW" becomes zh-Hant (not matching).
    const max = new Intl.Locale(tag).maximize()
    return { tag: tag.toLowerCase(), language: max.language, script: max.script ?? '' }
  } catch {
    return null
  }
}

/**
 * Picks the best supported locale for a list of requested language tags in
 * priority order (for example `navigator.languages`). Each requested tag is
 * tried first as an exact match, then by language and script.
 */
export function matchLocale(
  requested: readonly string[],
  supported: readonly string[],
): string | undefined {
  const candidates = supported.flatMap((tag) => {
    const key = localeKey(tag)
    return key ? [{ supported: tag, key }] : []
  })
  for (const tag of requested) {
    const want = localeKey(tag)
    if (!want) continue
    const exact = candidates.find((c) => c.key.tag === want.tag)
    if (exact) return exact.supported
    const loose = candidates.find(
      (c) => c.key.language === want.language && c.key.script === want.script,
    )
    if (loose) return loose.supported
  }
  return undefined
}
