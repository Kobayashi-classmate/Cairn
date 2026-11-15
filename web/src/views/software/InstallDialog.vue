<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import {
  NAlert,
  NButton,
  NCollapse,
  NCollapseItem,
  NModal,
  NRadio,
  NRadioGroup,
  NSpin,
  NTag,
  NText,
  NTooltip,
  useThemeVars,
} from 'naive-ui'
import { isAbortError, isStepUpRequired } from '@/api/client'
import type { Job } from '@/api/jobs'
import {
  fetchSoftwareSettings,
  installSoftware,
  probeSources,
  type DownloadPolicy,
  type Offer,
  type ProbeResult,
  type Repository,
  type SoftwareEntry,
  type SourceCandidate,
  type SourceSlot,
} from '@/api/software'
import { useNow } from '@/composables/useNow'
import { errorMessage, errorText } from '@/i18n/errors'
import { formatDuration, formatNumber, formatRelativeTime } from '@/i18n/format'
import { useSessionStore } from '@/stores/session'

const show = defineModel<boolean>('show', { required: true })
const props = defineProps<{
  entry: SoftwareEntry | null
  offer: Offer | null
  /** Catalog repositories by id, for naming repository slots. */
  repos: ReadonlyMap<string, Repository>
}>()
const emit = defineEmits<{ started: [job: Job] }>()

const { t, te } = useI18n()
const session = useSessionStore()
const themeVars = useThemeVars()
const now = useNow(60_000)

/** The download policy, shown before the probe answers (which repeats it). */
const policy = ref<DownloadPolicy | null>(null)

const probing = ref(false)
const result = ref<ProbeResult | null>(null)
const probeError = ref<unknown>(null)
/** Candidate chosen in the runtime download slot. */
const selected = ref<string | null>(null)
const installing = ref(false)
const installError = ref<unknown>(null)

let controller: AbortController | null = null

watch(show, (open) => {
  if (!open) {
    controller?.abort()
    return
  }
  result.value = null
  probeError.value = null
  installError.value = null
  selected.value = null
  void loadPolicy()
  void probe()
})

async function loadPolicy() {
  try {
    policy.value = (await fetchSoftwareSettings()).downloadPolicy
  } catch {
    // The probe result names the policy as well.
  }
}

onBeforeUnmount(() => controller?.abort())

/** Checks the download sources of the line; takes a few seconds. */
async function probe() {
  const { entry, offer } = props
  if (!entry || !offer) return
  controller?.abort()
  const current = new AbortController()
  controller = current
  probing.value = true
  probeError.value = null
  try {
    const probed = await probeSources([{ software: entry.id, line: offer.line }], {
      signal: current.signal,
    })
    result.value = probed
    policy.value = probed.policy
    selected.value = probed.slots.find((slot) => slot.selectable)?.selected ?? null
  } catch (err) {
    if (isAbortError(err)) return
    probeError.value = err
    result.value = null
  } finally {
    if (controller === current) {
      controller = null
      probing.value = false
    }
  }
}

/** The runtime download, the only slot whose source can be chosen. */
const downloadSlot = computed(() => result.value?.slots.find((slot) => slot.selectable) ?? null)
/** Repository and system slots, shown for information only. */
const infoSlots = computed(() => result.value?.slots.filter((slot) => !slot.selectable) ?? [])

const policyLabel = computed(() => (policy.value ? label('software.policy', `${policy.value}.short`) : ''))

function label(prefix: string, value: string): string {
  const key = `${prefix}.${value}`
  return te(key) ? t(key) : value
}

function slotTitle(slot: SourceSlot): string {
  if (slot.kind === 'system') {
    return slot.role === 'security' ? t('software.probe.slot.security') : t('software.probe.slot.system')
  }
  if (slot.kind === 'repo') return props.repos.get(slot.repo ?? '')?.name ?? slot.repo ?? slot.id
  return t('software.probe.slot.download')
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

async function install() {
  const { entry, offer } = props
  if (!entry || !offer) return
  installing.value = true
  installError.value = null
  try {
    // Only a runtime download can be pointed at a source; other lines use their repositories.
    const source = downloadSlot.value && selected.value ? selected.value : undefined
    emit('started', await installSoftware(entry.id, offer.line, source))
    show.value = false
  } catch (err) {
    if (!isStepUpRequired(err)) installError.value = err
  } finally {
    installing.value = false
  }
}
</script>

<template>
  <NModal
    v-model:show="show"
    preset="card"
    :title="entry && offer ? t('software.install.title', { name: entry.name, line: offer.line }) : ''"
    class="install-dialog"
  >
    <div class="policy">
      <NText v-if="policyLabel" depth="3">
        {{ t('software.install.policy', { policy: policyLabel }) }}
      </NText>
      <RouterLink
        v-if="session.can('settings.read')"
        :to="{ name: 'settings-panel' }"
        class="policy__link"
        @click="show = false"
      >
        {{ t('software.install.changePolicy') }}
      </RouterLink>
    </div>

    <div v-if="probing" class="probing">
      <NSpin size="small" />
      <span>{{ t('software.install.probing') }}</span>
    </div>

    <template v-if="result && !probing">
      <!-- The runtime download: choose where the official file comes from. -->
      <section v-if="downloadSlot" class="slot">
        <h4 class="slot__title">{{ slotTitle(downloadSlot) }}</h4>
        <dl class="slot__facts">
          <template v-if="downloadSlot.version">
            <dt>{{ t('software.install.version') }}</dt>
            <dd class="mono" dir="ltr">{{ downloadSlot.version }}</dd>
          </template>
          <template v-if="downloadSlot.file">
            <dt>{{ t('software.install.file') }}</dt>
            <dd class="mono" dir="ltr">{{ downloadSlot.file }}</dd>
          </template>
        </dl>
        <NAlert v-if="downloadSlot.error" type="error" class="block">
          <div>{{ slotErrorText(downloadSlot) }}</div>
          <code v-if="slotErrorReason(downloadSlot)" class="mono reason" dir="ltr">{{ slotErrorReason(downloadSlot) }}</code>
        </NAlert>
        <NAlert
          v-if="downloadSlot.warning"
          :type="downloadSlot.warning === 'no_eligible' ? 'error' : 'warning'"
          class="block"
        >
          {{ warningText(downloadSlot) }}
        </NAlert>
        <NRadioGroup v-model:value="selected" class="candidates">
          <NRadio
            v-for="candidate in split(downloadSlot).compared"
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
        <NCollapse v-if="split(downloadSlot).skipped.length > 0" class="skipped">
          <NCollapseItem
            :title="t('software.probe.skipped', { count: split(downloadSlot).skipped.length }, split(downloadSlot).skipped.length)"
            name="skipped"
          >
            <ul class="skipped__list">
              <li v-for="candidate in split(downloadSlot).skipped" :key="candidate.id">
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
        <NText v-if="slot.kind === 'system'" depth="3" tag="p" class="note">
          {{ t('software.probe.systemNote') }}
        </NText>
      </section>
    </template>

    <NAlert v-if="probeError && !probing" type="warning" :title="t('software.install.probeFailed')" class="block">
      {{ errorMessage(probeError) }}
    </NAlert>
    <NAlert v-if="installError" type="error" class="block">{{ errorMessage(installError) }}</NAlert>

    <div class="actions">
      <NButton :disabled="probing || installing" @click="probe">{{ t('software.install.checkAgain') }}</NButton>
      <span class="actions__spacer" />
      <NButton :disabled="installing" @click="show = false">{{ t('common.action.cancel') }}</NButton>
      <NButton type="primary" :loading="installing" @click="install">
        {{ t('software.action.install') }}
      </NButton>
    </div>
  </NModal>
</template>

<style scoped>
.install-dialog {
  inline-size: min(760px, calc(100vw - 32px));
}

.policy {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 4px 12px;
  margin-block-end: 16px;
  font-size: 13px;
}

.policy__link {
  color: inherit;
}

.probing {
  display: flex;
  align-items: center;
  gap: 10px;
  padding-block: 24px;
}

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

.actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-block-start: 8px;
}

.actions__spacer {
  flex: 1;
}
</style>
