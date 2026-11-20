<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, type HTMLAttributes } from 'vue'
import { useI18n } from 'vue-i18n'
import { Monitor, Moon, Sun } from '@lucide/vue'
import { toast } from 'vue-sonner'
import type { ThemeMode } from '@/api/session'
import { errorMessage } from '@/i18n/errors'
import { useSessionStore } from '@/stores/session'
import { useThemeStore } from '@/stores/theme'
import SegmentedControl from './SegmentedControl.vue'
import type { SegmentedOption } from './segmented'

// Light / dark / follow the system. Signed in, the choice is saved to the
// account; before sign-in it stays in this browser.
defineProps<{
  iconOnly?: boolean
  vertical?: boolean
  class?: HTMLAttributes['class']
}>()

const { t } = useI18n()
const theme = useThemeStore()
const session = useSessionStore()

const options = computed<SegmentedOption<ThemeMode>[]>(() => [
  { value: 'light', label: t('common.theme.light'), icon: Sun },
  { value: 'dark', label: t('common.theme.dark'), icon: Moon },
  { value: 'system', label: t('common.theme.system'), icon: Monitor },
])

async function choose(mode: ThemeMode) {
  try {
    await session.changeTheme(mode)
  } catch (err) {
    toast.error(t('common.theme.saveFailed'), { description: errorMessage(err) })
  }
}
</script>

<template>
  <SegmentedControl
    :model-value="theme.mode"
    :options="options"
    :label="t('common.theme.label')"
    :icon-only="iconOnly"
    :vertical="vertical"
    :class="$props.class"
    @update:model-value="choose"
  />
</template>
