<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { HTMLAttributes } from 'vue'
import { useI18n } from 'vue-i18n'
import { Languages } from '@lucide/vue'
import type { AcceptableValue } from 'reka-ui'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { useLocaleChoice } from '@/composables/useLocaleChoice'
import { localeNativeName } from '@/i18n'
import { cn } from '@/lib/utils'

// Language picker for the sign-in pages (and anywhere a field is needed).
// Each language is listed in its own language.
const props = defineProps<{
  id?: string
  size?: 'sm' | 'default'
  class?: HTMLAttributes['class']
  /** Accessible name when no <label> points at the control. */
  ariaLabel?: string
  /** Ids of help or error texts for the control. */
  describedBy?: string
}>()

const { t } = useI18n()
const { locale, choices, switching, select } = useLocaleChoice()

function onUpdate(value: AcceptableValue) {
  if (typeof value === 'string') void select(value)
}
</script>

<template>
  <Select :model-value="locale" :disabled="switching" @update:model-value="onUpdate">
    <SelectTrigger
      :id="id"
      :size="size ?? 'default'"
      :aria-label="ariaLabel ?? (id ? undefined : t('common.language.label'))"
      :aria-describedby="describedBy"
      :class="cn('min-w-36 max-md:data-[size=sm]:h-11', props.class)"
    >
      <Languages aria-hidden="true" />
      <SelectValue class="flex-1 text-start">
        <span :lang="locale">{{ localeNativeName(locale) }}</span>
      </SelectValue>
    </SelectTrigger>
    <SelectContent>
      <SelectItem v-for="choice in choices" :key="choice.code" :value="choice.code">
        <span :lang="choice.code">{{ choice.name }}</span>
      </SelectItem>
    </SelectContent>
  </Select>
</template>
