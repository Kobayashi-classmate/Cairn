// SPDX-License-Identifier: AGPL-3.0-only
import { dateEnUS, dateZhCN, enUS, zhCN, type NDateLocale, type NLocale } from 'naive-ui'

interface NaiveLocale {
  locale: NLocale
  dateLocale: NDateLocale
}

const ENGLISH: NaiveLocale = { locale: enUS, dateLocale: dateEnUS }

/**
 * Built-in strings of Naive UI components (date pickers, empty states, ...).
 * A locale missing here still works; those component strings stay in English.
 */
const NAIVE_LOCALES: Record<string, NaiveLocale> = {
  en: ENGLISH,
  'zh-CN': { locale: zhCN, dateLocale: dateZhCN },
}

export function naiveLocaleFor(locale: string): NaiveLocale {
  return NAIVE_LOCALES[locale] ?? ENGLISH
}
