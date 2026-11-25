<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { ExternalLink, RefreshCw } from '@lucide/vue'
import { isAbortError } from '@/api/client'
import { fetchJournal, type JournalEntry } from '@/api/logs'
import type { Offer } from '@/api/software'
import Card from '@/components/common/Card.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import SegmentedControl from '@/components/common/SegmentedControl.vue'
import SelectField from '@/components/common/SelectField.vue'
import { LogSpans } from '@/components/logs/LogText'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { formatLogTime } from '@/i18n/format'
import { TONE_TEXT } from '@/lib/tone'
import { cn } from '@/lib/utils'
import { useAnsiPalette } from '@/logs/ansiPalette'
import { priorityTone } from '@/logs/journal'
import { useSessionStore } from '@/stores/session'

// The system journal of the line's services: the newest 200 entries of one
// service, newest at the bottom.
const LIMIT = 200

const props = defineProps<{ offer: Offer }>()

const { t } = useI18n()
const session = useSessionStore()
/** The colours of messages with ANSI colours. */
const palette = useAnsiPalette()

const units = computed(() => (props.offer.units ?? []).map((unit) => unit.name))
const unit = ref(units.value[0] ?? '')
const entries = ref<JournalEntry[] | null>(null)
const error = ref<unknown>(null)
const loading = ref(false)
const box = ref<HTMLElement | null>(null)
let controller: AbortController | null = null

/** The catalog names services without a suffix ("php8.4-fpm"); the
 * journal filter wants the full unit name. */
function unitName(name: string): string {
  return /\.(service|socket|timer|scope|mount|target|path)$/.test(name) ? name : `${name}.service`
}

async function load() {
  controller?.abort()
  if (!unit.value) return
  const request = new AbortController()
  controller = request
  loading.value = true
  error.value = null
  try {
    const journal = await fetchJournal({ unit: unitName(unit.value), limit: LIMIT }, request.signal)
    entries.value = journal.entries
    void nextTick(() => {
      if (box.value) box.value.scrollTop = box.value.scrollHeight
    })
  } catch (err) {
    if (isAbortError(err)) return
    error.value = err
  } finally {
    if (controller === request) {
      controller = null
      loading.value = false
    }
  }
}

watch(
  unit,
  () => {
    entries.value = null
    void load()
  },
  { immediate: true },
)
onBeforeUnmount(() => controller?.abort())

const unitOptions = computed(() => units.value.map((name) => ({ value: name, label: name })))

/** Text colour of an entry's level (errors, warnings, debug). */
function toneText(priority: number): string {
  const tone = priorityTone(priority)
  return tone ? TONE_TEXT[tone] : ''
}

/** The level of entries shown in a colour, so that the colour is not the only sign of it. */
function levelLabel(priority: number): string {
  return priorityTone(priority) ? t(`logs.priorityShort.${priority}`) : ''
}
</script>

<template>
  <Card :level="3" flush>
    <template #actions>
      <SegmentedControl
        v-if="units.length > 1 && units.length <= 3"
        v-model="unit"
        :options="unitOptions"
        :label="t('software.logs.unit')"
        class="font-mono"
      />
      <SelectField
        v-else-if="units.length > 3"
        v-model="unit"
        :options="unitOptions"
        :aria-label="t('software.logs.unit')"
        class="w-56 font-mono"
      />
      <span v-else class="font-mono text-detail text-muted-foreground" dir="ltr">{{ unit }}</span>
      <Button variant="ghost" size="sm" :disabled="loading" @click="load">
        <RefreshCw :class="loading ? 'animate-spin' : ''" aria-hidden="true" />
        {{ t('common.action.refresh') }}
      </Button>
      <RouterLink
        v-if="unit && session.can('logs.read')"
        :to="{ name: 'logs', query: { unit: unitName(unit) } }"
        class="inline-flex items-center gap-1.5 text-detail font-medium text-link underline-offset-4 hover:underline max-md:min-h-11"
      >
        <ExternalLink class="size-3.5" aria-hidden="true" />
        {{ t('logs.openInLogs') }}
      </RouterLink>
    </template>

    <ErrorState v-if="error && !entries" :error="error" compact @retry="load" />
    <div v-else-if="!entries" class="flex flex-col gap-2 px-4 py-4" aria-hidden="true">
      <Skeleton v-for="row in 6" :key="row" class="h-3.5" :style="{ width: `${92 - row * 7}%` }" />
    </div>
    <p v-else-if="entries.length === 0" class="m-0 px-4 py-4 text-detail text-muted-foreground">{{ t('software.logs.empty') }}</p>
    <div v-else ref="box" class="max-h-[min(65vh,620px)] overflow-auto" role="log" tabindex="0" :aria-label="t('software.logs.label', { unit })">
      <ol class="m-0 list-none p-0 py-2 font-mono text-xs leading-relaxed" dir="ltr" :style="palette">
        <li v-for="(entry, index) in entries" :key="index" class="flex gap-3 px-4 py-0.5 hover:bg-accent/50">
          <span class="shrink-0 text-muted-foreground tabular-nums">{{ formatLogTime(entry.time / 1000) }}</span>
          <span class="min-w-0 flex-1 whitespace-pre-wrap [overflow-wrap:anywhere]">
            <span v-if="levelLabel(entry.priority)" :class="cn('font-semibold', toneText(entry.priority))">{{ levelLabel(entry.priority) }} </span>
            <span v-if="entry.identifier" class="text-muted-foreground">{{ entry.identifier }}{{ entry.pid ? `[${entry.pid}]` : '' }}: </span>
            <LogSpans :text="entry.message" :tone="priorityTone(entry.priority)" />
          </span>
        </li>
      </ol>
    </div>
  </Card>
  <p v-if="entries && entries.length >= LIMIT" class="m-0 text-xs text-muted-foreground">{{ t('software.logs.limited', { count: LIMIT }) }}</p>
</template>
