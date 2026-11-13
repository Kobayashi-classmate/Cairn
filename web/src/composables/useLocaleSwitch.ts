// SPDX-License-Identifier: AGPL-3.0-only
import { computed, h, ref, type VNodeChild } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMessage, type SelectOption } from 'naive-ui'
import { localeNativeName } from '@/i18n'
import { errorMessage } from '@/i18n/errors'
import { useSessionStore } from '@/stores/session'

/** Shared state for every language picker: options, current value and switching. */
export function useLocaleSwitch() {
  const { locale } = useI18n()
  const session = useSessionStore()
  const message = useMessage()
  const switching = ref(false)

  // Each language is listed in its own language, whatever the UI language is.
  const options = computed<SelectOption[]>(() =>
    session.supportedLocales.map((code) => ({ value: code, label: localeNativeName(code) })),
  )

  /** Marks each option with its language so it is rendered and read correctly. */
  function renderLabel(option: SelectOption): VNodeChild {
    return h('span', { lang: String(option.value) }, String(option.label))
  }

  async function select(code: string): Promise<void> {
    switching.value = true
    try {
      await session.changeLocale(code)
    } catch (err) {
      message.error(errorMessage(err))
    } finally {
      switching.value = false
    }
  }

  return { locale, options, renderLabel, switching, select }
}
