// SPDX-License-Identifier: AGPL-3.0-only
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { localeNativeName } from '@/i18n'
import { errorMessage } from '@/i18n/errors'
import { useSessionStore } from '@/stores/session'

/** A language the UI can switch to, named in that language. */
export interface LocaleChoice {
  code: string
  name: string
}

/**
 * Language switching for the new components (the Naive UI pages keep
 * useLocaleSwitch). Failures are reported as a toast.
 */
export function useLocaleChoice() {
  const { locale } = useI18n()
  const session = useSessionStore()
  const switching = ref(false)

  const choices = computed<LocaleChoice[]>(() =>
    session.supportedLocales.map((code) => ({ code, name: localeNativeName(code) })),
  )

  async function select(code: string): Promise<void> {
    if (code === locale.value) return
    switching.value = true
    try {
      await session.changeLocale(code)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      switching.value = false
    }
  }

  return { locale, choices, switching, select }
}
