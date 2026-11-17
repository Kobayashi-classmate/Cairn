<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NCollapse,
  NCollapseItem,
  NRadio,
  NRadioGroup,
  NTag,
  NText,
  NTooltip,
  useThemeVars,
} from 'naive-ui'
import type { Repository, SourceCandidate, SourceSlot } from '@/api/software'
import { useNow } from '@/composables/useNow'
import { errorText } from '@/i18n/errors'
import { formatBytes, formatDuration, formatNumber, formatRelativeTime } from '@/i18n/format'

/**
 * The download sources of a probe: runtime downloads with a choice of
 * candidate each, then repository and system slots for information only.
 * Shared by the install dialog and the first-login wizard.
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
const themeVars = useThemeVars()
const now = useNow(60_000)

/** Runtime downloads, whose source can be chosen. */
const downloadSlots = computed(() => props.slots.filter((slot) => slot.selectable))
/** Repository and system slots, shown for information only. */
const infoSlots = computed(() => props.slots.filter((slot) => !slot.selectable))

function choose(slotId: string, candidateId: string) {
  selection.value = { ...selection.value, [slotId]: candidateId }
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
</script>

<template>
  <div class="source-slots">
    <!-- The runtime download: choose where the official file comes from. -->
    <section v-for="slot in downloadSlots" :key="slot.id" class="slot">
      <h4 class="slot__title">{{ slotTitle(slot) }}</h4>
      <dl class="slot__facts">
        <template v-if="slot.version">
          <dt>{{ t('software.install.version') }}</dt>
          <dd class="mono" dir="ltr">{{ slot.version }}</dd>
        </template>
        <template v-if="slot.file">
          <dt>{{ t('software.install.file') }}</dt>
          <dd class="mono" dir="ltr">{{ slot.file }}</dd>
        </template>
        <template v-if="slot.size">
          <dt>{{ t('software.install.size') }}</dt>
          <dd>{{ formatBytes(slot.size) }}</dd>
        </template>
      </dl>
      <NAlert v-if="slot.error" type="error" class="block">
        <div>{{ slotErrorText(slot) }}</div>
        <code v-if="slotErrorReason(slot)" class="mono reason" dir="ltr">{{ slotErrorReason(slot) }}</code>
      </NAlert>
      <NAlert
        v-if="slot.warning"
        :type="slot.warning === 'no_eligible' ? 'error' : 'warning'"
        class="block"
      >
        {{ warningText(slot) }}
      </NAlert>
      <NRadioGroup
        :value="selection[slot.id] ?? null"
        class="candidates"
        @update:value="(id: string) => choose(slot.id, id)"
      >
        <NRadio
          v-for="candidate in split(slot).compared"
          :key="candidate.id"
          :value="candidate.id"
          :disabled="!candidate.eligible"
          class="candidate"
        >
          <span class="candidate__line">
            <span class="candidate__name">{{ candidate.name }}</span>
            <NTag size="small" :bordered="false">{{ label('software.probe.kind', candidate.kind) }}</NTag>
            <NTag v-if="candidate.recommended" size="small" type="success" :bordered="false">
              {{ t('software.recommended') }}
            </NTag>
          </span>
          <span class="candidate__meta">
            <NTooltip v-if="candidate.status === 'unreachable' && candidate.error">
              <template #trigger>
                <span class="status status--bad">{{ statusText(candidate) }}</span>
              </template>
              <span class="mono" dir="ltr">{{ candidate.error }}</span>
            </NTooltip>
            <span v-else class="status" :class="{ 'status--bad': !candidate.eligible }">
              {{ statusText(candidate) }}
            </span>
            <span>{{ latency(candidate) }}</span>
            <span>{{ speed(candidate) }}</span>
          </span>
        </NRadio>
      </NRadioGroup>
      <NCollapse v-if="split(slot).skipped.length > 0" class="skipped">
        <NCollapseItem
          :title="t('software.probe.skipped', { count: split(slot).skipped.length }, split(slot).skipped.length)"
          :name="`${slot.id}:skipped`"
        >
          <ul class="skipped__list">
            <li v-for="candidate in split(slot).skipped" :key="candidate.id">
              {{ candidate.name }} · {{ label('software.probe.kind', candidate.kind) }} · {{ latency(candidate) }}
            </li>
          </ul>
        </NCollapseItem>
      </NCollapse>
    </section>

    <!-- Repositories and system sources: shown for information only. -->
    <section v-for="slot in infoSlots" :key="slot.id" class="slot">
      <h4 class="slot__title">{{ slotTitle(slot) }}</h4>
      <NAlert v-if="slot.error" type="error" class="block">
        <div>{{ slotErrorText(slot) }}</div>
        <code v-if="slotErrorReason(slot)" class="mono reason" dir="ltr">{{ slotErrorReason(slot) }}</code>
      </NAlert>
      <NAlert v-if="slot.warning" :type="slot.warning === 'no_eligible' ? 'error' : 'warning'" class="block">
        {{ warningText(slot) }}
      </NAlert>
      <div v-if="split(slot).compared.length > 0" class="table-wrap">
        <table class="sources">
          <thead>
            <tr>
              <th>{{ t('software.probe.column.name') }}</th>
              <th>{{ t('software.probe.column.kind') }}</th>
              <th>{{ t('software.probe.column.status') }}</th>
              <th>{{ t('software.probe.column.indexTime') }}</th>
              <th>{{ t('software.probe.column.latency') }}</th>
              <th>{{ t('software.probe.column.speed') }}</th>
              <th v-if="switchable && slot.kind === 'system'" />
            </tr>
          </thead>
          <tbody>
            <tr v-for="candidate in split(slot).compared" :key="candidate.id">
              <td>
                <span class="candidate__name">{{ candidate.name }}</span>
                <NTag v-if="candidate.inUse" size="small" type="info" :bordered="false" class="in-use">
                  {{ t('software.probe.inUse') }}
                </NTag>
              </td>
              <td>{{ label('software.probe.kind', candidate.kind) }}</td>
              <td>
                <NTooltip v-if="candidate.status === 'unreachable' && candidate.error">
                  <template #trigger>
                    <span class="status status--bad">{{ statusText(candidate) }}</span>
                  </template>
                  <span class="mono" dir="ltr">{{ candidate.error }}</span>
                </NTooltip>
                <span v-else class="status" :class="{ 'status--bad': !candidate.eligible }">
                  {{ statusText(candidate) }}
                </span>
              </td>
              <td>{{ indexTime(candidate) }}</td>
              <td>{{ latency(candidate) }}</td>
              <td>{{ speed(candidate) }}</td>
              <td v-if="switchable && slot.kind === 'system'">
                <NButton
                  v-if="candidate.eligible && !candidate.inUse"
                  size="tiny"
                  :type="candidate.recommended ? 'primary' : 'default'"
                  :disabled="switchDisabled"
                  @click="emit('switch', slot, candidate)"
                >
                  {{ t('software.probe.use') }}
                </NButton>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <NCollapse v-if="split(slot).skipped.length > 0" class="skipped">
        <NCollapseItem
          :title="t('software.probe.skipped', { count: split(slot).skipped.length }, split(slot).skipped.length)"
          :name="slot.id"
        >
          <ul class="skipped__list">
            <li v-for="candidate in split(slot).skipped" :key="candidate.id">
              {{ candidate.name }} · {{ label('software.probe.kind', candidate.kind) }} · {{ latency(candidate) }}
            </li>
          </ul>
        </NCollapseItem>
      </NCollapse>
      <NText v-if="slot.kind === 'system' && !switchable" depth="3" tag="p" class="note">
        {{ t('software.probe.systemNote') }}
      </NText>
    </section>
  </div>
</template>

<style scoped>
.slot {
  margin-block-end: 20px;
}

.slot__title {
  margin: 0 0 8px;
  font-size: 15px;
  font-weight: 600;
}

.slot__facts {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 2px 12px;
  margin: 0 0 12px;
  font-size: 13px;
}

.slot__facts dt {
  opacity: 0.7;
}

.slot__facts dd {
  margin: 0;
  overflow-wrap: anywhere;
}

.block {
  margin-block-end: 12px;
}

.candidates {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.candidate__line {
  display: inline-flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}

.candidate__name {
  font-weight: 500;
}

.candidate__meta {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 16px;
  margin-block-start: 2px;
  font-size: 12px;
  opacity: 0.8;
}

.status--bad {
  color: v-bind('themeVars.errorColor');
}

.table-wrap {
  overflow-x: auto;
}

.sources {
  inline-size: 100%;
  border-collapse: collapse;
  font-size: 13px;
}

.sources th,
.sources td {
  padding: 6px 8px;
  border-block-end: 1px solid rgb(128 128 128 / 15%);
  text-align: start;
  white-space: nowrap;
}

.sources th {
  font-weight: 500;
  opacity: 0.7;
}

.in-use {
  margin-inline-start: 6px;
}

.skipped {
  margin-block-start: 8px;
}

.skipped__list {
  margin: 0;
  padding-inline-start: 1.2em;
  font-size: 13px;
}

.note {
  margin: 8px 0 0;
  font-size: 13px;
}

.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}

.reason {
  display: block;
  margin-block-start: 6px;
  font-size: 12px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
