<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ShieldCheck } from '@lucide/vue'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { useNow } from '@/composables/useNow'
import { formatTime } from '@/i18n/format'
import { cn } from '@/lib/utils'
import { useSessionStore } from '@/stores/session'

// Shown while sensitive actions do not ask for the password again.
const props = defineProps<{ compact?: boolean }>()

const { t } = useI18n()
const session = useSessionStore()
const now = useNow(5000)

const active = computed(() => session.elevatedUntil > now.value)
const tooltip = computed(() =>
  t('common.elevated.tooltip', { time: formatTime(session.elevatedUntil, false) }),
)
</script>

<template>
  <Tooltip v-if="active">
    <TooltipTrigger as-child>
      <span
        tabindex="0"
        role="status"
        :aria-label="props.compact ? tooltip : undefined"
        :class="
          cn(
            'inline-flex items-center gap-1 rounded-full bg-success-soft text-caption text-success outline-none focus-visible:ring-2 focus-visible:ring-ring/50',
            props.compact ? 'size-7 justify-center' : 'px-2 py-px',
          )
        "
      >
        <ShieldCheck class="size-3.5" aria-hidden="true" />
        <span v-if="!props.compact">{{ t('common.elevated.label') }}</span>
      </span>
    </TooltipTrigger>
    <TooltipContent side="right">{{ tooltip }}</TooltipContent>
  </Tooltip>
</template>
