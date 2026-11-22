<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { Check, LoaderCircle } from '@lucide/vue'
import type { AcceptableValue } from 'reka-ui'
import { toast } from 'vue-sonner'
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
import CairnLogo from '@/components/common/CairnLogo.vue'
import LocaleSelect from '@/components/common/LocaleSelect.vue'
import Notice from '@/components/common/Notice.vue'
import ThemeSwitcher from '@/components/common/ThemeSwitcher.vue'
import DownloadEstimate from '@/components/DownloadEstimate.vue'
import JobStateTag from '@/components/JobStateTag.vue'
import SourceSlots from '@/components/SourceSlots.vue'
import TotpEnrollment from '@/components/TotpEnrollment.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
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

/** Reports a failed request; a declined re-authentication is not an error. */
function showError(err: unknown) {
  if (isAbortError(err) || isStepUpRequired(err)) return
  toast.error(errorMessage(err))
}

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

/** Checkbox state of a suite: on, off or partly ticked. */
function suiteState(suite: Suite): boolean | 'indeterminate' {
  if (suiteChecked(suite)) return true
  return suiteIndeterminate(suite) ? 'indeterminate' : false
}

function onLine(id: string, value: AcceptableValue) {
  if (typeof value === 'string') chooseLine(id, value)
}
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

const stepLabels = computed(() => STEPS.map((id) => ({ id, label: t(`wizard.steps.${id}`) })))
const progressPercent = computed(() =>
  job.value?.progress != null ? Math.round(job.value.progress * 100) : null,
)

const logTail = computed(() => log.value.split('\n').filter(Boolean).slice(-LOG_TAIL_LINES).join('\n'))
const jobFinal = computed(() => (job.value ? isFinalState(job.value.state) : false))
const jobErrorText = computed(() => {
  const error = job.value?.error
  return error ? (errorText(error.code, error.params) ?? error.code) : ''
})
</script>

<template>
  <div class="min-h-dvh bg-background text-foreground">
    <header class="flex flex-wrap items-center gap-2.5 px-4 py-3 sm:px-6">
      <span class="flex items-center gap-2.5">
        <CairnLogo />
        <span class="text-base font-bold tracking-tight">{{ t('common.app.name') }}</span>
      </span>
      <span class="flex-1" />
      <LocaleSelect size="sm" />
      <ThemeSwitcher icon-only />
      <Button v-if="step !== 'start'" variant="ghost" size="sm" :disabled="leaving" @click="leave('skipped')">
        {{ t('wizard.skip') }}
      </Button>
    </header>

    <main class="mx-auto w-full max-w-[960px] px-4 pt-4 pb-12 sm:px-6">
      <h1 class="m-0 text-page-title">{{ t('wizard.title') }}</h1>
      <p class="mt-1 mb-6 text-muted-foreground">{{ t('wizard.intro') }}</p>

      <ol class="m-0 mb-6 flex list-none flex-wrap gap-x-6 gap-y-2 p-0">
        <li
          v-for="(item, index) in stepLabels"
          :key="item.id"
          class="flex items-center gap-2 text-detail"
          :aria-current="index + 1 === stepIndex ? 'step' : undefined"
        >
          <span
            class="flex size-6 shrink-0 items-center justify-center rounded-full border text-caption tabular-nums"
            :class="
              index + 1 < stepIndex
                ? 'border-primary bg-primary text-primary-foreground'
                : index + 1 === stepIndex
                  ? 'border-primary text-primary'
                  : 'border-border text-muted-foreground'
            "
            aria-hidden="true"
          >
            <Check v-if="index + 1 < stepIndex" class="size-3.5" />
            <template v-else>{{ index + 1 }}</template>
          </span>
          <span :class="index + 1 === stepIndex ? 'font-semibold' : 'text-muted-foreground'">{{ item.label }}</span>
        </li>
      </ol>

      <!-- Step 1: two-step verification -->
      <section v-if="step === 'security'" class="rounded-lg border border-border bg-card p-5">
        <h2 class="m-0 mb-3 text-section">{{ t('wizard.security.title') }}</h2>
        <Notice v-if="totpOn" tone="success" class="mb-3">{{ t('wizard.security.on') }}</Notice>
        <template v-else>
          <p class="m-0 mb-3">{{ t('wizard.security.why') }}</p>
          <p class="m-0 mb-3 text-muted-foreground">{{ t('wizard.security.how') }}</p>
          <div v-if="enrolling">
            <TotpEnrollment @done="enrolling = false" @cancel="enrolling = false" />
          </div>
        </template>
        <div v-if="!enrolling" class="mt-4 flex flex-wrap items-center gap-2">
          <Button v-if="!totpOn" @click="enrolling = true">{{ t('wizard.security.setUp') }}</Button>
          <Button v-if="!totpOn" variant="outline" @click="step = 'software'">{{ t('wizard.security.later') }}</Button>
          <Button v-else @click="step = 'software'">{{ t('wizard.continue') }}</Button>
        </div>
      </section>

      <!-- Step 2: software -->
      <template v-else-if="step === 'software'">
        <Notice v-if="loadError" tone="destructive" class="mb-3">{{ errorMessage(loadError) }}</Notice>
        <div v-if="!catalog && !loadError" class="grid gap-4 sm:grid-cols-2" role="status">
          <span class="sr-only">{{ t('common.boot.loading') }}</span>
          <Skeleton v-for="n in 2" :key="n" class="h-36 rounded-lg" />
        </div>
        <template v-else-if="catalog">
          <p class="m-0 mb-3 text-muted-foreground">{{ t('wizard.software.intro') }}</p>
          <div class="grid gap-4 sm:grid-cols-2">
            <section v-for="suite in suites" :key="suite.id" class="rounded-lg border border-border bg-card p-4">
              <div class="flex items-center gap-2">
                <Checkbox
                  :id="`suite-${suite.id}`"
                  :model-value="suiteState(suite)"
                  :disabled="suiteTargets(suite).length === 0"
                  @update:model-value="(on) => toggleSuite(suite, on === true)"
                />
                <label :for="`suite-${suite.id}`" class="font-semibold">{{ t(`wizard.suite.${suite.id}.name`) }}</label>
              </div>
              <p class="mt-1.5 mb-2 text-detail text-muted-foreground">{{ t(`wizard.suite.${suite.id}.description`) }}</p>
              <ul class="m-0 flex list-disc flex-col gap-0.5 ps-5 text-detail">
                <li v-for="(item, index) in suite.items" :key="index">
                  <span class="inline-flex flex-wrap items-center gap-1.5">
                    <span :class="{ 'text-muted-foreground': item.missing }">{{ suiteItemLabel(item) }}</span>
                    <span v-if="item.missing" class="text-muted-foreground">{{ t('wizard.software.missing') }}</span>
                    <Badge v-else-if="item.software && item.line && installed(item.software, item.line)">
                      {{ t('software.installed') }}
                    </Badge>
                  </span>
                </li>
              </ul>
            </section>
          </div>

          <h2 class="mt-6 mb-3 text-section">{{ t('wizard.software.other') }}</h2>
          <ul class="m-0 mb-2 grid list-none gap-x-6 gap-y-2 p-0 sm:grid-cols-2">
            <li v-for="entry in entries" :key="entry.id" class="flex items-center gap-2">
              <Checkbox
                :id="`software-${entry.id}`"
                :model-value="isTicked(entry.id)"
                :disabled="installed(entry.id, lineOf(entry.id)) || (full && !isTicked(entry.id))"
                @update:model-value="(on) => setTicked(entry.id, on === true)"
              />
              <label :for="`software-${entry.id}`" class="min-w-0 truncate">{{ entry.name }}</label>
              <Badge v-if="installed(entry.id, lineOf(entry.id))">{{ t('software.installed') }}</Badge>
              <Select :model-value="lineOf(entry.id)" @update:model-value="(value) => onLine(entry.id, value)">
                <SelectTrigger
                  size="sm"
                  class="ms-auto w-[150px]"
                  :aria-label="t('wizard.software.lineOf', { name: entry.name })"
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem v-for="option in lineOptions(entry)" :key="option.value" :value="option.value">
                    {{ option.label }}
                  </SelectItem>
                </SelectContent>
              </Select>
            </li>
          </ul>

          <p class="m-0 text-detail text-muted-foreground" role="status">
            {{ t('wizard.software.count', { count: items.length, max: MAX_PROBE_ITEMS }) }}
          </p>
          <div class="mt-4 flex flex-wrap items-center gap-2">
            <Button v-if="route.query.step !== 'software'" variant="outline" @click="step = 'security'">
              {{ t('wizard.back') }}
            </Button>
            <span class="flex-1" />
            <Button variant="outline" :disabled="leaving" @click="leave('done')">{{ t('wizard.software.nothing') }}</Button>
            <Button :disabled="items.length === 0" @click="toConfirm">{{ t('wizard.continue') }}</Button>
          </div>
        </template>
      </template>

      <!-- Step 3: confirm -->
      <section v-else-if="step === 'confirm'" class="rounded-lg border border-border bg-card p-5">
        <h2 class="m-0 mb-3 text-section">{{ t('wizard.confirm.title') }}</h2>
        <div class="mb-4 flex flex-wrap gap-1.5">
          <Badge v-for="item in items" :key="item.software" variant="secondary">{{ itemLabel(item) }}</Badge>
        </div>

        <div v-if="probing" class="flex items-center gap-2.5 py-5 text-muted-foreground" role="status">
          <LoaderCircle class="size-4 animate-spin" aria-hidden="true" />
          <span>{{ t('software.install.probing') }}</span>
        </div>
        <div v-else-if="result" class="legacy-ui">
          <SourceSlots v-model:selection="selection" :slots="result.slots" :repos="repos" :names="names" />
          <h3 class="mt-4 mb-2 text-section">{{ t('wizard.confirm.estimate') }}</h3>
          <DownloadEstimate :result="result" :selection="selection" class="mb-3" />
        </div>
        <Notice v-if="probeError && !probing" tone="warning" class="mb-3">
          {{ t('software.install.probeFailed') }} {{ errorMessage(probeError) }}
        </Notice>
        <Notice v-if="startError" tone="destructive" class="mb-3">{{ errorMessage(startError) }}</Notice>

        <div class="mt-4 flex flex-wrap items-center gap-2">
          <Button variant="outline" :disabled="starting" @click="backToSoftware">{{ t('wizard.back') }}</Button>
          <Button variant="outline" :disabled="probing || starting" @click="probe">
            {{ t('software.install.checkAgain') }}
          </Button>
          <span class="flex-1" />
          <Button :disabled="starting || items.length === 0" @click="startInstall">
            <LoaderCircle v-if="starting" class="animate-spin" aria-hidden="true" />
            {{ t('wizard.confirm.start') }}
          </Button>
        </div>
      </section>

      <!-- Step 4: started -->
      <section v-else class="rounded-lg border border-border bg-card p-5">
        <h2 class="m-0 mb-3 text-section">{{ t('wizard.start.title') }}</h2>
        <p class="m-0 mb-3">{{ t('wizard.start.background') }}</p>
        <template v-if="job">
          <div class="mb-3 flex items-center gap-3">
            <span class="legacy-ui"><JobStateTag :state="job.state" /></span>
            <RouterLink :to="{ name: 'job', params: { id: job.id } }" class="text-link underline-offset-2 hover:underline">
              {{ t('software.viewOutput') }}
            </RouterLink>
          </div>
          <div
            v-if="progressPercent !== null && !jobFinal"
            class="mb-3 flex items-center gap-3"
          >
            <div
              class="h-2 flex-1 overflow-hidden rounded-full bg-track"
              role="progressbar"
              :aria-valuenow="progressPercent"
              aria-valuemin="0"
              aria-valuemax="100"
              :aria-label="t('wizard.start.title')"
            >
              <div class="h-full rounded-full bg-primary transition-[width]" :style="{ width: `${progressPercent}%` }" />
            </div>
            <span class="text-detail tabular-nums">{{ formatPercent(progressPercent, 0) }}</span>
          </div>
          <Notice v-if="job.state === 'succeeded'" tone="success" class="mb-3">{{ t('wizard.start.done') }}</Notice>
          <Notice v-else-if="jobFinal && jobErrorText" tone="destructive" class="mb-3">{{ jobErrorText }}</Notice>
          <pre
            v-if="logTail"
            class="m-0 max-h-80 overflow-auto rounded-md bg-muted px-3 py-2.5 font-mono text-xs leading-relaxed break-words whitespace-pre-wrap"
            dir="ltr"
          >{{ logTail }}</pre>
        </template>
        <div v-else class="flex items-center gap-2.5 text-muted-foreground" role="status">
          <LoaderCircle class="size-4 animate-spin" aria-hidden="true" />
          <span class="sr-only">{{ t('common.boot.loading') }}</span>
        </div>
        <div class="mt-4 flex flex-wrap items-center gap-2">
          <span class="flex-1" />
          <Button @click="router.replace(homeLocation(router))">{{ t('wizard.start.overview') }}</Button>
        </div>
      </section>
    </main>
  </div>
</template>
