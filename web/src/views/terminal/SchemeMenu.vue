<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// The web terminal's colour scheme, saved to the account. Each choice shows
// a strip of its colours as they look in the panel's current mode; only the
// Cairn scheme is checked to keep every colour readable, as the note says.
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Palette } from '@lucide/vue'
import type { AcceptableValue } from 'reka-ui'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { errorMessage } from '@/i18n/errors'
import { useSessionStore } from '@/stores/session'
import { useThemeStore } from '@/stores/theme'
import { isTerminalSchemeId } from '@/terminal/schemeIds'
import { TERMINAL_SCHEMES, schemeTheme } from '@/terminal/schemes'

const { t } = useI18n()
const session = useSessionStore()
const theme = useThemeStore()

/** The colours that tell schemes apart. */
const SWATCHES = ['red', 'green', 'yellow', 'blue', 'magenta', 'cyan'] as const

const items = computed(() =>
  TERMINAL_SCHEMES.map((scheme) => {
    const colors = schemeTheme(scheme.id, theme.isDark)
    return {
      id: scheme.id,
      name: scheme.id === 'cairn' ? t('terminal.scheme.cairn') : scheme.name,
      darkOnly: !scheme.followsMode,
      background: colors.background,
      swatches: SWATCHES.map((name) => colors[name] ?? ''),
    }
  }),
)

async function choose(value: AcceptableValue) {
  if (!isTerminalSchemeId(value)) return
  try {
    await session.changeTerminalScheme(value)
  } catch (err) {
    toast.error(t('terminal.scheme.saveFailed'), { description: errorMessage(err) })
  }
}
</script>

<template>
  <DropdownMenu>
    <DropdownMenuTrigger as-child>
      <Button variant="outline" class="max-md:h-11">
        <Palette aria-hidden="true" />
        {{ t('terminal.scheme.button') }}
      </Button>
    </DropdownMenuTrigger>
    <DropdownMenuContent align="end" class="w-72 max-w-[calc(100vw-2rem)]">
      <DropdownMenuLabel>{{ t('terminal.scheme.label') }}</DropdownMenuLabel>
      <DropdownMenuRadioGroup :model-value="session.terminalScheme" @update:model-value="choose">
        <DropdownMenuRadioItem v-for="item in items" :key="item.id" :value="item.id" class="max-md:min-h-11">
          <span class="min-w-0 flex-1 truncate">{{ item.name }}</span>
          <span v-if="item.darkOnly" class="shrink-0 text-xs text-muted-foreground">{{ t('terminal.scheme.darkOnly') }}</span>
          <span
            class="flex h-5 shrink-0 items-center gap-0.5 rounded-sm border px-1"
            :style="{ backgroundColor: item.background }"
            aria-hidden="true"
          >
            <span v-for="(color, index) in item.swatches" :key="index" class="size-2 rounded-full" :style="{ backgroundColor: color }" />
          </span>
        </DropdownMenuRadioItem>
      </DropdownMenuRadioGroup>
      <DropdownMenuSeparator />
      <p class="px-2 py-1.5 text-xs text-muted-foreground">{{ t('terminal.scheme.note') }}</p>
    </DropdownMenuContent>
  </DropdownMenu>
</template>
