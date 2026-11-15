<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import {
  NAlert,
  NButton,
  NCard,
  NCheckbox,
  NH1,
  NH3,
  NProgress,
  NSelect,
  NSpin,
  NStep,
  NSteps,
  NTag,
  NText,
} from 'naive-ui'
import { isAbortError, isStepUpRequired } from '@/api/client'
import {
  MAX_PROBE_ITEMS,
  fetchSoftware,
  fetchSuites,
  installSoftwareSet,
  isAvailable,
  probeSources,
  runtimeSlotId,
  type InstallSetItem,
  type ProbeResult,
  type SoftwareCatalog,
  type SoftwareEntry,
  type Suite,
} from '@/api/software'
import { saveWizardState, type WizardState } from '@/api/wizard'
import DownloadEstimate from '@/components/DownloadEstimate.vue'
import JobStateTag from '@/components/JobStateTag.vue'
import LocaleSwitcher from '@/components/LocaleSwitcher.vue'
import SourceSlots from '@/components/SourceSlots.vue'
import TotpEnrollment from '@/components/TotpEnrollment.vue'
import { useErrorToast } from '@/composables/useErrorToast'
import { useJobFollow } from '@/composables/useJobFollow'
import { errorMessage, errorText } from '@/i18n/errors'
import { formatPercent } from '@/i18n/format'
import { isFinalState } from '@/jobs/labels'
import { homeLocation } from '@/layouts/navigation'
import { useSessionStore } from '@/stores/session'

/** Log lines shown while the installation runs; the job page has the rest. */
const LOG_TAIL_LINES = 14

type Step = 'security' | 'software' | 'confirm' | 'start'
const STEPS: readonly Step[] = ['security', 'software', 'confirm', 'start']

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const session = useSessionStore()
const showError = useErrorToast()

// Opened from the software page, the wizard starts at the software step.
const step = ref<Step>(route.query.step === 'software' ? 'software' : 'security')
const stepIndex = computed(() => STEPS.indexOf(step.value) + 1)

const leaving = ref(false)

/** Records how the wizard ended (only users who may change settings can) and leaves it. */
async function leave(state: WizardState | null) {
  leaving.value = true
  try {
    if (state && session.can('settings.manage')) await saveWizardState(state)
  } catch (err) {
    showError(err)
  } finally {
    leaving.value = false
  }
  await router.replace(homeLocation(router))
}

// ---- Step 1: two-step verification -------------------------------------

const enrolling = ref(false)
const totpOn = computed(() => session.user?.totpEnabled === true)

// ---- Step 2: software ----------------------------------------------------

const catalog = ref<SoftwareCatalog | null>(null)
const suites = ref<Suite[]>([])
const loadError = ref<unknown>(null)

onMounted(async () => {
  try {
    const [software, suiteList] = await Promise.all([fetchSoftware(), fetchSuites()])
    catalog.value = software
    suites.value = suiteList
  } catch (err) {
    loadError.value = err
  }
})

/** Entries with at least one line that can be installed here. */
const entries = computed(() => (catalog.value?.software ?? []).filter((e) => e.offers.some(isAvailable)))
const entryById = computed(() => new Map(entries.value.map((e) => [e.id, e])))
const names = computed(() => Object.fromEntries(entries.value.map((e) => [e.id, e.name])))
const repos = computed(() => new Map((catalog.value?.repos ?? []).map((repo) => [repo.id, repo])))

/** Chosen line per software (the recommended one until changed). */
const lineChoice = reactive<Record<string, string>>({})
/** Ticked software ids, in the order they were ticked. */
const ticked = ref<string[]>([])

function recommendedLine(entry: SoftwareEntry): string {
  const available = entry.offers.filter(isAvailable)
  return (available.find((o) => o.recommended) ?? available[0])?.line ?? ''
}

function lineOf(id: string): string {
  const entry = entryById.value.get(id)
  return lineChoice[id] ?? (entry ? recommendedLine(entry) : '')
}

function installed(id: string, line: string): boolean {
  return entryById.value.get(id)?.offers.some((o) => o.line === line && o.installed) === true
}

function lineOptions(entry: SoftwareEntry) {
  return entry.offers.filter(isAvailable).map((o) => ({
    value: o.line,
    label: o.recommended ? t('wizard.software.recommendedLine', { line: o.line }) : o.line,
  }))
}

const isTicked = (id: string) => ticked.value.includes(id)
const full = computed(() => ticked.value.length >= MAX_PROBE_ITEMS)

function setTicked(id: string, on: boolean) {
  if (on && !isTicked(id) && !full.value && !installed(id, lineOf(id))) {
    ticked.value = [...ticked.value, id]
  } else if (!on) {
    ticked.value = ticked.value.filter((x) => x !== id)
  }
}

function chooseLine(id: string, line: string) {
  lineChoice[id] = line
  // An installed line cannot be installed again.
  if (installed(id, line)) setTicked(id, false)
}

/** Suite items that can still be ticked: offered here and not installed yet. */
function suiteTargets(suite: Suite) {
  return suite.items.filter(
    (item): item is typeof item & { software: string; line: string } =>
      !item.missing && !!item.software && !!item.line && !installed(item.software, item.line),
  )
}

function suiteChecked(suite: Suite): boolean {
  const targets = suiteTargets(suite)
  return targets.length > 0 && targets.every((i) => isTicked(i.software) && lineOf(i.software) === i.line)
}

function suiteIndeterminate(suite: Suite): boolean {
  return !suiteChecked(suite) && suiteTargets(suite).some((i) => isTicked(i.software))
}

function toggleSuite(suite: Suite, on: boolean) {
  for (const item of suiteTargets(suite)) {
    if (on) {
      lineChoice[item.software] = item.line
      setTicked(item.software, true)
    } else {
      setTicked(item.software, false)
    }
  }
}

function suiteItemLabel(item: Suite['items'][number]): string {
  if (item.missing || !item.software) {
    return item.choices.map((id) => names.value[id] ?? id).join(' / ')
  }
  return `${names.value[item.software] ?? item.software} ${item.line ?? ''}`.trim()
}

/** What will be installed: ticked lines that are not installed. */
const items = computed<InstallSetItem[]>(() =>
  ticked.value
    .map((id) => ({ software: id, line: lineOf(id) }))
    .filter((item) => item.line && !installed(item.software, item.line)),
)

function itemLabel(item: InstallSetItem): string {
  return `${names.value[item.software] ?? item.software} ${item.line}`
}

// ---- Step 3: confirm -----------------------------------------------------

const probing = ref(false)
const result = ref<ProbeResult | null>(null)
const probeError = ref<unknown>(null)
/** Chosen candidate per runtime slot id. */
const selection = ref<Record<string, string>>({})
let probeController: AbortController | null = null

async function probe() {
  probeController?.abort()
  const current = new AbortController()
  probeController = current
  probing.value = true
  probeError.value = null
  try {
    const probed = await probeSources(
      items.value.map(({ software, line }) => ({ software, line })),
      { signal: current.signal },
    )
    result.value = probed
    selection.value = Object.fromEntries(
      probed.slots.filter((slot) => slot.selectable && slot.selected).map((slot) => [slot.id, slot.selected!]),
    )
  } catch (err) {
    if (isAbortError(err)) return
    probeError.value = err
    result.value = null
  } finally {
    if (probeController === current) {
      probeController = null
      probing.value = false
    }
  }
}

function toConfirm() {
  step.value = 'confirm'
  result.value = null
  selection.value = {}
  void probe()
}

function backToSoftware() {
  probeController?.abort()
  step.value = 'software'
}

// ---- Step 4: start -------------------------------------------------------

const starting = ref(false)
const startError = ref<unknown>(null)
const { job, log, start: followJob } = useJobFollow()

async function startInstall() {
  starting.value = true
  startError.value = null
  try {
    const started = await installSoftwareSet(
      items.value.map((item) => {
        // Runtime downloads use the chosen source; package lines use their repositories.
        const source = selection.value[runtimeSlotId(item.software, item.line)]
        return source ? { ...item, source } : item
      }),
    )
    step.value = 'start'
    followJob(started.id)
    if (session.can('settings.manage')) {
      await saveWizardState('done').catch((err: unknown) => showError(err))
    }
  } catch (err) {
    if (!isStepUpRequired(err)) startError.value = err
  } finally {
    starting.value = false
  }
}

const logTail = computed(() => log.value.split('\n').filter(Boolean).slice(-LOG_TAIL_LINES).join('\n'))
const jobFinal = computed(() => (job.value ? isFinalState(job.value.state) : false))
const jobErrorText = computed(() => {
  const error = job.value?.error
  return error ? (errorText(error.code, error.params) ?? error.code) : ''
})
</script>

<template>
  <div class="wizard">
    <header class="wizard__top">
      <span class="wizard__brand">{{ t('common.app.name') }}</span>
      <span class="wizard__spacer" />
      <LocaleSwitcher />
      <NButton v-if="step !== 'start'" quaternary size="small" :loading="leaving" @click="leave('skipped')">
        {{ t('wizard.skip') }}
      </NButton>
    </header>

    <main class="wizard__main">
      <NH1 class="wizard__title">{{ t('wizard.title') }}</NH1>
      <NText depth="3" tag="p" class="wizard__intro">{{ t('wizard.intro') }}</NText>

      <NSteps :current="stepIndex" size="small" class="wizard__steps">
        <NStep :title="t('wizard.steps.security')" />
        <NStep :title="t('wizard.steps.software')" />
        <NStep :title="t('wizard.steps.confirm')" />
        <NStep :title="t('wizard.steps.start')" />
      </NSteps>

      <!-- Step 1: two-step verification -->
      <NCard v-if="step === 'security'" size="small">
        <NH3 class="card-title">{{ t('wizard.security.title') }}</NH3>
        <template v-if="totpOn">
          <NAlert type="success" class="block">{{ t('wizard.security.on') }}</NAlert>
        </template>
        <template v-else>
          <NText tag="p" class="para">{{ t('wizard.security.why') }}</NText>
          <NText depth="3" tag="p" class="para">{{ t('wizard.security.how') }}</NText>
          <TotpEnrollment v-if="enrolling" @done="enrolling = false" @cancel="enrolling = false" />
        </template>
        <div v-if="!enrolling" class="actions">
          <NButton v-if="!totpOn" type="primary" @click="enrolling = true">
            {{ t('wizard.security.setUp') }}
          </NButton>
          <NButton v-if="!totpOn" @click="step = 'software'">{{ t('wizard.security.later') }}</NButton>
          <NButton v-else type="primary" @click="step = 'software'">{{ t('wizard.continue') }}</NButton>
        </div>
      </NCard>

      <!-- Step 2: software -->
      <template v-else-if="step === 'software'">
        <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>
        <NSpin :show="!catalog && !loadError">
          <NText depth="3" tag="p" class="para">{{ t('wizard.software.intro') }}</NText>
          <div class="suites">
            <NCard v-for="suite in suites" :key="suite.id" size="small" class="suite">
              <NCheckbox
                :checked="suiteChecked(suite)"
                :indeterminate="suiteIndeterminate(suite)"
                :disabled="suiteTargets(suite).length === 0"
                @update:checked="(on: boolean) => toggleSuite(suite, on)"
              >
                <span class="suite__name">{{ t(`wizard.suite.${suite.id}.name`) }}</span>
              </NCheckbox>
              <NText depth="3" tag="p" class="suite__description">
                {{ t(`wizard.suite.${suite.id}.description`) }}
              </NText>
              <ul class="suite__items">
                <li v-for="(item, index) in suite.items" :key="index">
                  <span :class="{ muted: item.missing }">{{ suiteItemLabel(item) }}</span>
                  <NText v-if="item.missing" depth="3" class="small">{{ t('wizard.software.missing') }}</NText>
                  <NTag
                    v-else-if="item.software && item.line && installed(item.software, item.line)"
                    size="small"
                    type="info"
                    :bordered="false"
                  >
                    {{ t('software.installed') }}
                  </NTag>
                </li>
              </ul>
            </NCard>
          </div>

          <NH3 class="section-title">{{ t('wizard.software.other') }}</NH3>
          <ul class="others">
            <li v-for="entry in entries" :key="entry.id" class="other">
              <NCheckbox
                :checked="isTicked(entry.id)"
                :disabled="installed(entry.id, lineOf(entry.id)) || (full && !isTicked(entry.id))"
                @update:checked="(on: boolean) => setTicked(entry.id, on)"
              >
                {{ entry.name }}
              </NCheckbox>
              <NSelect
                :value="lineOf(entry.id)"
                :options="lineOptions(entry)"
                size="small"
                class="other__line"
                @update:value="(line: string) => chooseLine(entry.id, line)"
              />
              <NTag v-if="installed(entry.id, lineOf(entry.id))" size="small" type="info" :bordered="false">
                {{ t('software.installed') }}
              </NTag>
            </li>
          </ul>

          <NText depth="3" tag="p" class="para small">
            {{ t('wizard.software.count', { count: items.length, max: MAX_PROBE_ITEMS }) }}
          </NText>
          <div class="actions">
            <NButton v-if="route.query.step !== 'software'" @click="step = 'security'">{{ t('wizard.back') }}</NButton>
            <span class="actions__spacer" />
            <NButton :loading="leaving" @click="leave('done')">{{ t('wizard.software.nothing') }}</NButton>
            <NButton type="primary" :disabled="items.length === 0" @click="toConfirm">
              {{ t('wizard.continue') }}
            </NButton>
          </div>
        </NSpin>
      </template>

      <!-- Step 3: confirm -->
      <NCard v-else-if="step === 'confirm'" size="small">
        <NH3 class="card-title">{{ t('wizard.confirm.title') }}</NH3>
        <div class="chosen">
          <NTag v-for="item in items" :key="item.software" :bordered="false">{{ itemLabel(item) }}</NTag>
        </div>

        <div v-if="probing" class="probing">
          <NSpin size="small" />
          <span>{{ t('software.install.probing') }}</span>
        </div>
        <template v-else-if="result">
          <SourceSlots v-model:selection="selection" :slots="result.slots" :repos="repos" :names="names" />
          <NH3 class="section-title">{{ t('wizard.confirm.estimate') }}</NH3>
          <DownloadEstimate :result="result" :selection="selection" class="block" />
        </template>
        <NAlert v-if="probeError && !probing" type="warning" :title="t('software.install.probeFailed')" class="block">
          {{ errorMessage(probeError) }}
        </NAlert>
        <NAlert v-if="startError" type="error" class="block">{{ errorMessage(startError) }}</NAlert>

        <div class="actions">
          <NButton :disabled="starting" @click="backToSoftware">{{ t('wizard.back') }}</NButton>
          <NButton :disabled="probing || starting" @click="probe">{{ t('software.install.checkAgain') }}</NButton>
          <span class="actions__spacer" />
          <NButton type="primary" :loading="starting" :disabled="items.length === 0" @click="startInstall">
            {{ t('wizard.confirm.start') }}
          </NButton>
        </div>
      </NCard>

      <!-- Step 4: started -->
      <NCard v-else size="small">
        <NH3 class="card-title">{{ t('wizard.start.title') }}</NH3>
        <NText tag="p" class="para">{{ t('wizard.start.background') }}</NText>
        <template v-if="job">
          <div class="job-line">
            <JobStateTag :state="job.state" />
            <RouterLink :to="{ name: 'job', params: { id: job.id } }">{{ t('software.viewOutput') }}</RouterLink>
          </div>
          <NProgress
            v-if="job.progress !== null && !jobFinal"
            type="line"
            :percentage="Math.round(job.progress * 100)"
            class="block"
          >
            {{ formatPercent(job.progress * 100, 0) }}
          </NProgress>
          <NAlert v-if="job.state === 'succeeded'" type="success" class="block">{{ t('wizard.start.done') }}</NAlert>
          <NAlert v-else-if="jobFinal && jobErrorText" type="error" class="block">{{ jobErrorText }}</NAlert>
          <pre v-if="logTail" class="log" dir="ltr">{{ logTail }}</pre>
        </template>
        <NSpin v-else size="small" />
        <div class="actions">
          <span class="actions__spacer" />
          <NButton type="primary" @click="router.replace(homeLocation(router))">{{ t('wizard.start.overview') }}</NButton>
        </div>
      </NCard>
    </main>
  </div>
</template>

<style scoped>
.wizard {
  min-block-size: 100vh;
  min-block-size: 100dvh;
}

.wizard__top {
  display: flex;
  align-items: center;
  gap: 8px;
  padding-block: 8px;
  padding-inline: 16px;
}

.wizard__brand {
  font-weight: 600;
  opacity: 0.8;
}

.wizard__spacer,
.actions__spacer {
  flex: 1;
}

.wizard__main {
  max-inline-size: 960px;
  margin-inline: auto;
  padding-block: 8px 48px;
  padding-inline: 16px;
}

.wizard__title {
  margin-block: 8px 4px;
  font-size: 26px;
}

.wizard__intro {
  margin-block: 0 20px;
}

.wizard__steps {
  margin-block-end: 24px;
}

.card-title,
.section-title {
  margin-block: 0 12px;
}

.section-title {
  margin-block-start: 24px;
}

.para {
  margin-block: 0 12px;
}

.block {
  margin-block-end: 12px;
}

.small {
  font-size: 13px;
}

.muted {
  opacity: 0.6;
}

.actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-block-start: 16px;
}

.suites {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 16px;
}

.suite__name {
  font-weight: 600;
}

.suite__description {
  margin-block: 6px 8px;
  font-size: 13px;
}

.suite__items {
  margin: 0;
  padding-inline-start: 1.2em;
}

.suite__items li {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  margin-block: 2px;
}

.others {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
  gap: 8px 24px;
  margin: 0 0 8px;
  padding: 0;
  list-style: none;
}

.other {
  display: flex;
  align-items: center;
  gap: 8px;
}

.other__line {
  inline-size: 150px;
  margin-inline-start: auto;
}

.chosen {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-block-end: 16px;
}

.probing {
  display: flex;
  align-items: center;
  gap: 10px;
  padding-block: 20px;
}

.job-line {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-block-end: 12px;
}

.log {
  max-block-size: 320px;
  margin: 0;
  padding: 10px 12px;
  overflow: auto;
  border-radius: 4px;
  background: rgb(128 128 128 / 12%);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  line-height: 1.5;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
