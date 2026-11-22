<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, type HTMLAttributes } from 'vue'
import { useI18n } from 'vue-i18n'
import { Monitor, Moon, Sun } from '@lucide/vue'
import { useMediaQuery } from '@vueuse/core'
import { RadioGroupIndicator, RadioGroupItem, RadioGroupRoot, type AcceptableValue } from 'reka-ui'
import { toast } from 'vue-sonner'
import { isThemeMode, type ThemeMode } from '@/api/session'
import { errorMessage } from '@/i18n/errors'
import { cn } from '@/lib/utils'
import { useSessionStore } from '@/stores/session'
import { useThemeStore } from '@/stores/theme'
import SegmentedControl from './SegmentedControl.vue'
import type { SegmentedOption } from './segmented'

// Light / dark / follow the system. Signed in, the choice is saved to the
// account; before sign-in it stays in this browser. `cards` shows the
// choices as radio cards with a small preview of each (settings ›
// Appearance) instead of the segmented switch of the sidebar.
const props = defineProps<{
  iconOnly?: boolean
  vertical?: boolean
  cards?: boolean
  class?: HTMLAttributes['class']
}>()

const { t } = useI18n()
const theme = useThemeStore()
const session = useSessionStore()
const systemDark = useMediaQuery('(prefers-color-scheme: dark)')

const options = computed<SegmentedOption<ThemeMode>[]>(() => [
  { value: 'light', label: t('common.theme.light'), icon: Sun },
  { value: 'dark', label: t('common.theme.dark'), icon: Moon },
  { value: 'system', label: t('common.theme.system'), icon: Monitor },
])

/** The preview panes of a card: the system choice shows both halves. */
function panes(mode: ThemeMode): ('light' | 'dark')[] {
  return mode === 'system' ? ['light', 'dark'] : [mode]
}

function hint(mode: ThemeMode): string {
  if (mode !== 'system') return t(`common.theme.hint.${mode}`)
  return t(systemDark.value ? 'common.theme.hint.systemDark' : 'common.theme.hint.systemLight')
}

async function choose(mode: ThemeMode) {
  try {
    await session.changeTheme(mode)
  } catch (err) {
    toast.error(t('common.theme.saveFailed'), { description: errorMessage(err) })
  }
}

function onCard(value: AcceptableValue) {
  if (isThemeMode(value)) void choose(value)
}
</script>

<template>
  <RadioGroupRoot
    v-if="cards"
    :model-value="theme.mode"
    :aria-label="t('common.theme.label')"
    :class="cn('grid gap-3 sm:grid-cols-3', props.class)"
    @update:model-value="onCard"
  >
    <RadioGroupItem
      v-for="option in options"
      :key="option.value"
      :value="option.value"
      class="group flex min-w-0 flex-col gap-3 rounded-lg border bg-card p-2.5 text-start outline-none transition-colors hover:bg-accent/50 focus-visible:ring-2 focus-visible:ring-ring/50 data-[state=checked]:border-primary data-[state=checked]:ring-1 data-[state=checked]:ring-primary"
    >
      <span class="flex h-[84px] w-full overflow-hidden rounded-md border" aria-hidden="true">
        <span
          v-for="pane in panes(option.value)"
          :key="pane"
          :class="cn(pane, 'flex min-w-0 flex-1')"
        >
          <span class="flex w-[32%] flex-col gap-[5px] border-e border-sidebar-border bg-sidebar px-[7px] py-[9px]">
            <span class="h-1.5 w-[70%] rounded-full bg-primary" />
            <span class="h-[5px] w-[85%] rounded-full bg-track" />
            <span class="h-[5px] w-[60%] rounded-full bg-track" />
            <span class="h-[5px] w-[75%] rounded-full bg-track" />
          </span>
          <span class="flex min-w-0 flex-1 flex-col gap-[7px] bg-background p-[9px]">
            <span class="h-[7px] w-[45%] rounded-full bg-foreground/70" />
            <span class="h-7 rounded-[5px] border border-border bg-card" />
            <span class="h-[5px] w-[70%] rounded-full bg-track" />
          </span>
        </span>
      </span>
      <span class="flex items-center gap-2.5 px-0.5 pb-0.5">
        <span
          class="inline-flex size-4 shrink-0 items-center justify-center rounded-full border-[1.5px] border-muted-foreground group-data-[state=checked]:border-primary"
          aria-hidden="true"
        >
          <RadioGroupIndicator class="size-2 rounded-full bg-primary" />
        </span>
        <span class="flex min-w-0 flex-col">
          <span class="font-semibold">{{ option.label }}</span>
          <span class="text-xs text-muted-foreground">{{ hint(option.value) }}</span>
        </span>
      </span>
    </RadioGroupItem>
  </RadioGroupRoot>
  <SegmentedControl
    v-else
    :model-value="theme.mode"
    :options="options"
    :label="t('common.theme.label')"
    :icon-only="iconOnly"
    :vertical="vertical"
    :class="props.class"
    @update:model-value="choose"
  />
</template>
