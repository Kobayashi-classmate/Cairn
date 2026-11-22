<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronRight } from '@lucide/vue'
import { RadioGroupIndicator, RadioGroupItem, RadioGroupRoot, type AcceptableValue } from 'reka-ui'
import type { Repository, SourceCandidate, SourceSlot } from '@/api/software'
import Notice from '@/components/common/Notice.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { useNow } from '@/composables/useNow'
import { errorText } from '@/i18n/errors'
import { formatBytes, formatDuration, formatNumber, formatRelativeTime } from '@/i18n/format'
import { cn } from '@/lib/utils'

/**
 * The download sources of a probe: runtime downloads with a choice of
 * candidate each, then repository and system slots for information only.
 * Shared by the install sheet, the first-login wizard and the host page.
 */
const props = defineProps<{
  slots: SourceSlot[]
  /** Catalog repositories by id, for naming repository slots. */
  repos: ReadonlyMap<string, Repository>
  /** Product names by catalog id, for naming runtime downloads. */
  names?: Readonly<Record<string, string>>
  /** System slots offer switching to another source (system management). */
  switchable?: boolean
  /** Switching is not possible right now (another change runs). */
  switchDisabled?: boolean
}>()
const emit = defineEmits<{ switch: [slot: SourceSlot, candidate: SourceCandidate] }>()

/** Chosen candidate id per runtime slot id. */
const selection = defineModel<Record<string, string>>('selection', { required: true })

const { t, te } = useI18n()
const now = useNow(60_000)

/** Runtime downloads, whose source can be chosen. */
const downloadSlots = computed(() => props.slots.filter((slot) => slot.selectable))
/** Repository and system slots, shown for information only. */
const infoSlots = computed(() => props.slots.filter((slot) => !slot.selectable))

function choose(slotId: string, value: AcceptableValue) {
  if (typeof value === 'string') selection.value = { ...selection.value, [slotId]: value }
}

function label(prefix: string, value: string): string {
  const key = `${prefix}.${value}`
  return te(key) ? t(key) : value
}

function slotTitle(slot: SourceSlot): string {
  if (slot.kind === 'system') {
    return slot.role === 'security' ? t('software.probe.slot.security') : t('software.probe.slot.system')
  }
  if (slot.kind === 'repo') return props.repos.get(slot.repo ?? '')?.name ?? slot.repo ?? slot.id
  // "runtime:<software>:<line>"
  const [, software = '', line = ''] = slot.id.split(':')
  const name = props.names?.[software]
  return name ? t('software.probe.slot.downloadOf', { name, line }) : t('software.probe.slot.download')
}

function statusText(candidate: SourceCandidate): string {
  if (candidate.status === 'behind') {
    return t('software.probe.status.behind', { duration: formatDuration(candidate.lagSeconds ?? 0, 1) })
  }
  return label('software.probe.status', candidate.status)
}

function latency(candidate: SourceCandidate): string {
  return candidate.latencyMs ? t('software.probe.ms', { value: formatNumber(candidate.latencyMs) }) : '—'
}

/** Speed in MB/s (decimal megabytes), or a dash when it was not measured. */
function speed(candidate: SourceCandidate): string {
  return candidate.speed ? t('software.probe.mbps', { value: formatNumber(candidate.speed / 1e6, 1) }) : '—'
}

function indexTime(candidate: SourceCandidate): string {
  return candidate.indexTime ? formatRelativeTime(candidate.indexTime, now.value) : '—'
}

/** Candidates that were compared, and the ones skipped because they answered slower. */
function split(slot: SourceSlot) {
  return {
    compared: slot.candidates.filter((c) => c.status !== 'skipped'),
    skipped: slot.candidates.filter((c) => c.status === 'skipped'),
  }
}

function warningText(slot: SourceSlot): string {
  if (slot.warning === 'in_use_behind') {
    const lag = slot.candidates.find((c) => c.inUse && c.status === 'behind')?.lagSeconds ?? 0
    return t('software.probe.warning.in_use_behind', { duration: formatDuration(lag, 1) })
  }
  return label('software.probe.warning', slot.warning ?? '')
}

function slotErrorText(slot: SourceSlot): string {
  return slot.error ? (errorText(slot.error.code, slot.error.params) ?? slot.error.code) : ''
}

function slotErrorReason(slot: SourceSlot): string {
  const reason = slot.error?.params?.reason
  return typeof reason === 'string' ? reason : ''
}

const statusClass = (candidate: SourceCandidate) => (candidate.eligible ? 'text-muted-foreground' : 'text-destructive')
</script>

<template>
  <div class="flex flex-col gap-5">
    <!-- The runtime download: choose where the official file comes from. -->
    <section v-for="slot in downloadSlots" :key="slot.id" class="flex flex-col gap-2.5">
      <h4 class="m-0 text-detail font-semibold">{{ slotTitle(slot) }}</h4>
      <dl class="m-0 grid grid-cols-[max-content_minmax(0,1fr)] gap-x-3 gap-y-0.5 text-detail">
        <template v-if="slot.version">
          <dt class="text-muted-foreground">{{ t('software.install.version') }}</dt>
          <dd class="m-0 font-mono text-[12.5px]" dir="ltr">{{ slot.version }}</dd>
        </template>
        <template v-if="slot.file">
          <dt class="text-muted-foreground">{{ t('software.install.file') }}</dt>
          <dd class="m-0 font-mono text-[12.5px] [overflow-wrap:anywhere]" dir="ltr">{{ slot.file }}</dd>
        </template>
        <template v-if="slot.size">
          <dt class="text-muted-foreground">{{ t('software.install.size') }}</dt>
          <dd class="m-0 tabular-nums">{{ formatBytes(slot.size) }}</dd>
        </template>
      </dl>
      <Notice v-if="slot.error" tone="destructive">
        {{ slotErrorText(slot) }}
        <code v-if="slotErrorReason(slot)" class="mt-1 block font-mono text-xs font-normal whitespace-pre-wrap [overflow-wrap:anywhere]" dir="ltr">
          {{ slotErrorReason(slot) }}
        </code>
      </Notice>
      <Notice v-if="slot.warning" :tone="slot.warning === 'no_eligible' ? 'destructive' : 'warning'">{{ warningText(slot) }}</Notice>
      <RadioGroupRoot
        :model-value="selection[slot.id]"
        :aria-label="slotTitle(slot)"
        class="flex flex-col gap-2"
        @update:model-value="(value: AcceptableValue) => choose(slot.id, value)"
      >
        <RadioGroupItem
          v-for="candidate in split(slot).compared"
          :key="candidate.id"
          :value="candidate.id"
          :disabled="!candidate.eligible"
          class="group flex w-full items-start gap-2.5 rounded-md border bg-card px-3 py-2.5 text-start outline-none transition-colors hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-60 data-[state=checked]:border-primary data-[state=checked]:bg-primary-soft"
        >
          <span
            class="mt-[3px] inline-flex size-4 shrink-0 items-center justify-center rounded-full border-[1.5px] border-muted-foreground group-data-[state=checked]:border-primary"
            aria-hidden="true"
          >
            <RadioGroupIndicator class="size-2 rounded-full bg-primary" />
          </span>
          <span class="flex min-w-0 flex-1 flex-col gap-0.5">
            <span class="flex flex-wrap items-center gap-1.5">
              <span class="font-medium">{{ candidate.name }}</span>
              <StatusPill>{{ label('software.probe.kind', candidate.kind) }}</StatusPill>
              <StatusPill v-if="candidate.recommended" tone="success">{{ t('software.recommended') }}</StatusPill>
            </span>
            <span class="flex flex-wrap gap-x-4 gap-y-0.5 text-xs tabular-nums">
              <span :class="statusClass(candidate)" :title="candidate.status === 'unreachable' ? candidate.error : undefined">
                {{ statusText(candidate) }}
              </span>
              <span class="text-muted-foreground">{{ latency(candidate) }}</span>
              <span class="text-muted-foreground">{{ speed(candidate) }}</span>
            </span>
          </span>
        </RadioGroupItem>
      </RadioGroupRoot>
      <details v-if="split(slot).skipped.length > 0" class="group text-detail">
        <summary class="inline-flex cursor-pointer list-none items-center gap-1 rounded-sm text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 [&::-webkit-details-marker]:hidden">
          <ChevronRight class="size-3.5 transition-transform group-open:rotate-90" aria-hidden="true" />
          {{ t('software.probe.skipped', { count: split(slot).skipped.length }, split(slot).skipped.length) }}
        </summary>
        <ul class="m-0 mt-1.5 flex list-none flex-col gap-0.5 p-0 ps-5 text-xs text-muted-foreground">
          <li v-for="candidate in split(slot).skipped" :key="candidate.id">
            {{ candidate.name }} · {{ label('software.probe.kind', candidate.kind) }} · {{ latency(candidate) }}
          </li>
        </ul>
      </details>
    </section>

    <!-- Repositories and system sources: shown for information only. -->
    <section v-for="slot in infoSlots" :key="slot.id" class="flex flex-col gap-2.5">
      <h4 class="m-0 text-detail font-semibold">{{ slotTitle(slot) }}</h4>
      <Notice v-if="slot.error" tone="destructive">
        {{ slotErrorText(slot) }}
        <code v-if="slotErrorReason(slot)" class="mt-1 block font-mono text-xs font-normal whitespace-pre-wrap [overflow-wrap:anywhere]" dir="ltr">
          {{ slotErrorReason(slot) }}
        </code>
      </Notice>
      <Notice v-if="slot.warning" :tone="slot.warning === 'no_eligible' ? 'destructive' : 'warning'">{{ warningText(slot) }}</Notice>
      <div v-if="split(slot).compared.length > 0" class="min-w-0 overflow-x-auto rounded-md border">
        <table class="w-full min-w-[520px] border-collapse text-detail">
          <thead>
            <tr class="bg-muted text-caption text-muted-foreground">
              <th scope="col" class="px-3 py-1.5 text-start font-medium">{{ t('software.probe.column.name') }}</th>
              <th scope="col" class="px-3 py-1.5 text-start font-medium">{{ t('software.probe.column.kind') }}</th>
              <th scope="col" class="px-3 py-1.5 text-start font-medium">{{ t('software.probe.column.status') }}</th>
              <th scope="col" class="px-3 py-1.5 text-start font-medium">{{ t('software.probe.column.indexTime') }}</th>
              <th scope="col" class="px-3 py-1.5 text-end font-medium">{{ t('software.probe.column.latency') }}</th>
              <th scope="col" class="px-3 py-1.5 text-end font-medium">{{ t('software.probe.column.speed') }}</th>
              <th v-if="switchable && slot.kind === 'system'" scope="col" class="px-3 py-1.5">
                <span class="sr-only">{{ t('common.field.action') }}</span>
              </th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="candidate in split(slot).compared" :key="candidate.id" class="border-t">
              <td class="px-3 py-1.5">
                <span class="inline-flex flex-wrap items-center gap-1.5">
                  <span class="font-medium">{{ candidate.name }}</span>
                  <StatusPill v-if="candidate.inUse" tone="progress">{{ t('software.probe.inUse') }}</StatusPill>
                </span>
              </td>
              <td class="px-3 py-1.5 whitespace-nowrap text-muted-foreground">{{ label('software.probe.kind', candidate.kind) }}</td>
              <td class="px-3 py-1.5">
                <Tooltip v-if="candidate.status === 'unreachable' && candidate.error">
                  <TooltipTrigger as-child>
                    <button type="button" class="rounded-sm text-destructive underline decoration-dotted underline-offset-2 outline-none focus-visible:ring-2 focus-visible:ring-ring/50">
                      {{ statusText(candidate) }}
                    </button>
                  </TooltipTrigger>
                  <TooltipContent><span class="font-mono" dir="ltr">{{ candidate.error }}</span></TooltipContent>
                </Tooltip>
                <span v-else :class="cn(statusClass(candidate), candidate.eligible && 'text-foreground')">{{ statusText(candidate) }}</span>
              </td>
              <td class="px-3 py-1.5 whitespace-nowrap text-muted-foreground">{{ indexTime(candidate) }}</td>
              <td class="px-3 py-1.5 text-end whitespace-nowrap tabular-nums text-muted-foreground">{{ latency(candidate) }}</td>
              <td class="px-3 py-1.5 text-end whitespace-nowrap tabular-nums text-muted-foreground">{{ speed(candidate) }}</td>
              <td v-if="switchable && slot.kind === 'system'" class="px-3 py-1.5 text-end">
                <Button
                  v-if="candidate.eligible && !candidate.inUse"
                  size="sm"
                  :variant="candidate.recommended ? 'default' : 'outline'"
                  :disabled="switchDisabled"
                  @click="emit('switch', slot, candidate)"
                >
                  {{ t('software.probe.use') }}
                </Button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <details v-if="split(slot).skipped.length > 0" class="group text-detail">
        <summary class="inline-flex cursor-pointer list-none items-center gap-1 rounded-sm text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 [&::-webkit-details-marker]:hidden">
          <ChevronRight class="size-3.5 transition-transform group-open:rotate-90" aria-hidden="true" />
          {{ t('software.probe.skipped', { count: split(slot).skipped.length }, split(slot).skipped.length) }}
        </summary>
        <ul class="m-0 mt-1.5 flex list-none flex-col gap-0.5 p-0 ps-5 text-xs text-muted-foreground">
          <li v-for="candidate in split(slot).skipped" :key="candidate.id">
            {{ candidate.name }} · {{ label('software.probe.kind', candidate.kind) }} · {{ latency(candidate) }}
          </li>
        </ul>
      </details>
      <p v-if="slot.kind === 'system' && !switchable" class="m-0 text-xs text-muted-foreground">
        {{ t('software.probe.systemNote') }}
      </p>
    </section>
  </div>
</template>
