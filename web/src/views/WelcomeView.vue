<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { Check, LoaderCircle } from '@lucide/vue'
import { toast } from 'vue-sonner'
import { isAbortError, isStepUpRequired } from '@/api/client'
import {
  fetchSoftware,
  fetchSuites,
  installSoftwareSet,
  probeSources,
  type InstallSetItem,
  type ProbeResult,
  type SoftwareCatalog,
  type Suite,
} from '@/api/software'
import { setStepUp } from '@/api/settings'
import { saveWizardState, type WizardState } from '@/api/wizard'
import CairnLogo from '@/components/common/CairnLogo.vue'
import LocaleSelect from '@/components/common/LocaleSelect.vue'
import Notice from '@/components/common/Notice.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import SwitchField from '@/components/common/SwitchField.vue'
import ThemeSwitcher from '@/components/common/ThemeSwitcher.vue'
import DownloadEstimate from '@/components/DownloadEstimate.vue'
import SourceSlots from '@/components/SourceSlots.vue'
import TotpEnrollment from '@/components/TotpEnrollment.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useJobFollow } from '@/composables/useJobFollow'
import { errorMessage, errorText } from '@/i18n/errors'
import { formatIsoDate, formatPercent } from '@/i18n/format'
import { isFinalState, jobStateLabel } from '@/jobs/labels'
import { jobTone } from '@/views/jobs/labels'
import SoftwarePicker from '@/views/wizard/SoftwarePicker.vue'
import { canChoose, offerOf, type PickedLine } from '@/views/wizard/selection'
import { chosenSource, defaultSelection } from '@/software/sources'
import { homeLocation } from '@/layouts/navigation'
import { useSessionStore } from '@/stores/session'
import { useSoftwareStore } from '@/stores/software'

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

/**
 * The panel's step-up switch, offered to whoever may change settings. The
 * setup left this session re-authenticated, so turning it off asks for
 * nothing here; the value being saved shows until the server answers.
 */
const canChangeStepUp = computed(() => session.can('settings.manage'))
const stepUpSaving = ref<boolean | null>(null)
const stepUpOn = computed(() => stepUpSaving.value ?? session.stepUpRequired)

async function chooseStepUp(value: boolean) {
  if (stepUpSaving.value !== null || value === session.stepUpRequired) return
  stepUpSaving.value = value
  try {
    await setStepUp(value)
  } catch (err) {
    if (!isStepUpRequired(err)) showError(err)
  } finally {
    stepUpSaving.value = null
  }
}

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

/** The whole catalog by id. */
const entryById = computed(() => new Map((catalog.value?.software ?? []).map((e) => [e.id, e])))
const names = computed(() => Object.fromEntries((catalog.value?.software ?? []).map((e) => [e.id, e.name])))
const repos = computed(() => new Map((catalog.value?.repos ?? []).map((repo) => [repo.id, repo])))

/** The selection of the software step: one line per software. */
const picked = ref<PickedLine[]>([])

/** What will be installed: the selected lines that can be installed and are not installed yet. */
const items = computed<PickedLine[]>(() =>
  picked.value.filter((item) => {
    const offer = offerOf(entryById.value, item.software, item.line)
    return offer !== undefined && canChoose(offer)
  }),
)

function itemLabel(item: PickedLine): string {
  return `${names.value[item.software] ?? item.software} ${item.line}`
}

// ---- Step 3: confirm -----------------------------------------------------

const probing = ref(false)
const result = ref<ProbeResult | null>(null)
const probeError = ref<unknown>(null)
/** Chosen candidate per selectable slot id (runtime downloads and the system repositories). */
const sources = ref<Record<string, string>>({})
let probeController: AbortController | null = null

/** Chosen lines past their upstream support: warned about before the installation starts. */
const endedItems = computed(() =>
  items.value.flatMap((item) => {
    const offer = offerOf(entryById.value, item.software, item.line)
    return offer?.endOfLife ? [{ ...item, eol: offer.eol ?? '' }] : []
  }),
)

function endOfLifeText(item: PickedLine & { eol: string }): string {
  const named = { name: names.value[item.software] ?? item.software, line: item.line }
  return item.eol
    ? t('wizard.confirm.endOfLife', { ...named, date: formatIsoDate(item.eol) })
    : t('wizard.confirm.endOfLifeUndated', named)
}

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
    sources.value = defaultSelection(probed.slots)
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
  sources.value = {}
  void probe()
}

function backToSoftware() {
  probeController?.abort()
  step.value = 'software'
}

/**
 * An item of the install set: runtimes carry their download's chosen
 * source, system packages the one shared system source (one transaction
 * has one main source), vendor lines none. A line past its upstream
 * support is acknowledged: the confirm step warned about it.
 */
function installItem(item: PickedLine): InstallSetItem {
  const offer = offerOf(entryById.value, item.software, item.line)
  const out: InstallSetItem = { software: item.software, line: item.line }
  const source = chosenSource(result.value, sources.value, item.software, item.line, offer?.method)
  if (source) out.source = source
  if (offer?.endOfLife) out.acceptEndOfLife = true
  return out
}

// ---- Step 4: start -------------------------------------------------------

const starting = ref(false)
const startError = ref<unknown>(null)
const { job, log, start: followJob } = useJobFollow()

async function startInstall() {
  starting.value = true
  startError.value = null
  try {
    const started = await installSoftwareSet(items.value.map(installItem))
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
// What is installed changed: source checks made before are out of date.
const softwareStore = useSoftwareStore()
watch(jobFinal, (done) => {
  if (done) softwareStore.invalidateProbes()
})
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
        <Notice v-if="totpOn && !enrolling" tone="success" class="mb-3">{{ t('wizard.security.on') }}</Notice>
        <template v-else-if="!totpOn">
          <p class="m-0 mb-3">{{ t('wizard.security.why') }}</p>
          <p class="m-0 mb-3 text-muted-foreground">{{ t('wizard.security.how') }}</p>
        </template>
        <!-- Mounted by `enrolling` alone: confirming turns totpOn on before the recovery codes are shown. -->
        <TotpEnrollment v-if="enrolling" @done="enrolling = false" @cancel="enrolling = false" />
        <div v-if="!enrolling && canChangeStepUp" class="mt-4 flex flex-col gap-3 border-t border-border pt-4">
          <SwitchField
            :model-value="stepUpOn"
            :label="t('settings.stepUp.title')"
            :hint="t('settings.stepUp.description')"
            :disabled="stepUpSaving !== null"
            @update:model-value="chooseStepUp"
          />
          <Notice v-if="!stepUpOn" tone="warning">{{ t('settings.stepUp.offWarning') }}</Notice>
        </div>
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
          <p class="m-0 mb-4 text-muted-foreground">{{ t('wizard.software.intro') }}</p>
          <SoftwarePicker v-model:picked="picked" :catalog="catalog" :suites="suites" />
          <div class="mt-6 flex flex-wrap items-center gap-2">
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
        <Notice v-for="item in endedItems" :key="`eol-${item.software}`" tone="warning" class="mb-3">
          {{ endOfLifeText(item) }}
        </Notice>

        <div v-if="probing" class="flex items-center gap-2.5 py-5 text-muted-foreground" role="status">
          <LoaderCircle class="size-4 animate-spin" aria-hidden="true" />
          <span>{{ t('software.install.probing') }}</span>
        </div>
        <div v-else-if="result">
          <SourceSlots v-model:selection="sources" :slots="result.slots" :repos="repos" :names="names" />
          <h3 class="mt-4 mb-2 text-section">{{ t('wizard.confirm.estimate') }}</h3>
          <DownloadEstimate :result="result" :selection="sources" class="mb-3" />
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
          <Button :disabled="starting || probing || items.length === 0" @click="startInstall">
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
            <StatusPill :tone="jobTone(job.state)" dot>{{ jobStateLabel(job.state) }}</StatusPill>
            <RouterLink :to="{ name: 'jobs', query: { open: `job:${job.id}`, section: 'log' } }" class="text-link underline-offset-2 hover:underline">
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
            tabindex="0"
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
