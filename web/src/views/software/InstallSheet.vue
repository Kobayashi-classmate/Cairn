<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RefreshCw } from '@lucide/vue'
import { isAbortError, isStepUpRequired } from '@/api/client'
import {
  DOWNLOAD_POLICIES,
  SYSTEM_MAIN_SLOT,
  installSoftware,
  isAvailable,
  runtimeSlotId,
  type DownloadPolicy,
  type LinePlan,
  type Offer,
  type ProbeResult,
} from '@/api/software'
import DownloadEstimate from '@/components/DownloadEstimate.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import OptionGroup from '@/components/common/OptionGroup.vue'
import SegmentedControl from '@/components/common/SegmentedControl.vue'
import type { SegmentedOption } from '@/components/common/segmented'
import type { OptionItem } from '@/components/common/types'
import SourceSlots from '@/components/SourceSlots.vue'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useClock } from '@/composables/useClock'
import { errorMessage } from '@/i18n/errors'
import { formatBytes, formatWhen } from '@/i18n/format'
import { categoryText, endOfLifeNotice, lineFacts } from '@/software/display'
import { chosenSource, defaultSelection } from '@/software/sources'
import { useSessionStore } from '@/stores/session'
import { useSoftwareStore, type CachedProbe } from '@/stores/software'

// "Install <software>" (interface spec, section 6: form sheet with "Will
// do"): pick the line, see where it would be downloaded from (measured, the
// recommended source preselected; the choice holds for this installation
// only) and the exact steps, then install as a job. A probe of the last
// 10 minutes is shown again when the sheet reopens or the line changes back;
// "Check again" measures anew. A probe in flight is abandoned when the line
// changes. A line past its upstream support can be chosen with a warning.
const { t } = useI18n()
const store = useSoftwareStore()
const session = useSessionStore()
const clock = useClock()

const open = computed({
  get: () => store.installOpen,
  set: (value: boolean) => (store.installOpen = value),
})

const entry = computed(() => (store.installTarget ? store.entry(store.installTarget.software) : null))
const line = ref('')
const offer = computed<Offer | null>(() => entry.value?.offers.find((o) => o.line === line.value) ?? null)

const probing = ref(false)
const result = ref<ProbeResult | null>(null)
/** When the shown result was measured, Unix milliseconds. */
const testedAt = ref<number | null>(null)
const probeError = ref<unknown>(null)
/** Chosen candidate per selectable slot id. */
const selection = ref<Record<string, string>>({})
const submitting = ref(false)
const error = ref<unknown>(null)
const savingPolicy = ref(false)
const policyError = ref<unknown>(null)
let controller: AbortController | null = null
/** The line the last probe was started for. */
let probedLine = ''

function installable(o: Offer): boolean {
  return isAvailable(o) && !o.installed
}

/** The line asked for, else the recommended one, else a supported one before one past its support. */
function firstLine(): string {
  const offers = entry.value?.offers ?? []
  const wanted = store.installTarget?.line
  const pick =
    offers.find((o) => o.line === wanted && installable(o)) ??
    offers.find((o) => o.recommended && installable(o)) ??
    offers.find((o) => installable(o) && !o.endOfLife) ??
    offers.find(installable)
  return pick?.line ?? ''
}

// Mounted on first use (already open), hence immediate.
watch(
  open,
  (value) => {
    if (!value) {
      controller?.abort()
      return
    }
    error.value = null
    policyError.value = null
    void refreshPolicy()
    line.value = firstLine()
    void probe()
  },
  { immediate: true },
)

watch(line, (value) => {
  if (open.value && value !== probedLine) void probe()
})

onBeforeUnmount(() => controller?.abort())

/** The download policy: as last known, else as the probe names it. */
const policy = computed<DownloadPolicy | null>(() => store.downloadPolicy ?? result.value?.policy ?? null)
const canSetPolicy = computed(() => session.can('software.manage'))

async function refreshPolicy() {
  try {
    const current = await store.loadPolicy()
    // A kept result from before the policy was changed elsewhere: measure again.
    if (open.value && result.value && result.value.policy !== current) void probe(true)
  } catch {
    // The probe result names the policy as well.
  }
}

/** Saves the panel-wide policy, then measures again: candidates and the recommended source depend on it. */
async function changePolicy(value: DownloadPolicy) {
  if (value === policy.value || savingPolicy.value) return
  savingPolicy.value = true
  policyError.value = null
  try {
    await store.savePolicy(value)
  } catch (err) {
    policyError.value = err
    return
  } finally {
    savingPolicy.value = false
  }
  if (open.value) void probe(true)
}

const policyOptions = computed<SegmentedOption<DownloadPolicy>[]>(() =>
  DOWNLOAD_POLICIES.map((value) => ({ value, label: t(`software.policy.${value}.short`) })),
)

/** Shows a probe result with the recommended candidates chosen. */
function show(probed: CachedProbe) {
  result.value = probed.result
  testedAt.value = probed.at
  selection.value = defaultSelection(probed.result.slots)
}

/**
 * Checks the download sources of the chosen line. A kept result of the
 * same line is shown at once unless `force`; a new probe takes a few
 * seconds, up to about 45.
 */
async function probe(force = false) {
  controller?.abort()
  controller = null
  probedLine = line.value
  probeError.value = null
  const current = entry.value
  const items = current && line.value ? [{ software: current.id, line: line.value }] : null
  const kept = items && !force ? store.peekProbe(items) : null
  probing.value = false
  if (kept) {
    show(kept)
    return
  }
  result.value = null
  testedAt.value = null
  selection.value = {}
  if (!items) return
  const request = new AbortController()
  controller = request
  probing.value = true
  try {
    const probed = await store.probeCached(items, { force, signal: request.signal })
    // Superseded meanwhile (another line, a kept result shown).
    if (controller === request) show(probed)
  } catch (err) {
    if (isAbortError(err) || controller !== request) return
    probeError.value = err
  } finally {
    if (controller === request) {
      controller = null
      probing.value = false
    }
  }
}

const checkedText = computed(() =>
  testedAt.value === null ? '' : t('software.install.checkedAt', { time: formatWhen(Math.floor(testedAt.value / 1000), clock.value) }),
)

const lineOptions = computed<OptionItem[]>(() =>
  (entry.value?.offers ?? []).map((o) => ({
    value: o.line,
    label: `${entry.value?.name ?? ''} ${o.line}`,
    hint: lineFacts(o),
    disabled: !installable(o),
  })),
)

const names = computed(() => (entry.value ? { [entry.value.id]: entry.value.name } : {}))

const repo = computed(() => (offer.value?.repo ? (store.repos.get(offer.value.repo) ?? null) : null))

/** The plan of the chosen line, from the probe. */
const plan = computed<LinePlan | null>(
  () => result.value?.plan?.find((p) => p.software === entry.value?.id && p.line === line.value) ?? null,
)

/** The chosen system source when it is not the one in use: the packages come from it this time. */
const otherSystemSource = computed(() => {
  const slot = result.value?.slots.find((s) => s.id === SYSTEM_MAIN_SLOT && s.selectable)
  const candidate = slot?.candidates.find((c) => c.id === selection.value[SYSTEM_MAIN_SLOT])
  return candidate && !candidate.inUse ? candidate.name : ''
})

function join(list: readonly string[]): string {
  return list.join(t('software.steps.separator'))
}

const steps = computed<string[]>(() => {
  const current = entry.value
  const chosen = offer.value
  if (!current || !chosen) return []
  const p = plan.value
  const list: string[] = []
  const repoName = (id?: string) => store.repos.get(id ?? '')?.name ?? id ?? ''
  if (!p) {
    // No plan (the probe failed or has not answered): what the catalog says.
    if (chosen.method === 'standalone') list.push(t('software.steps.downloadLine', { name: current.name, line: chosen.line }))
    else if (chosen.packages?.length) list.push(t('software.steps.packages', { packages: join(chosen.packages) }))
    if (chosen.services?.length) list.push(t('software.steps.services', { services: join(chosen.services) }))
    return list
  }
  if (p.method === 'vendor' && !p.repoReady) list.push(t('software.steps.addRepo', { repo: repoName(p.repo) }))
  if (p.stream) list.push(t('software.steps.stream', { stream: p.stream }))
  if (p.method === 'standalone') {
    const slot = result.value?.slots.find((s) => s.id === runtimeSlotId(current.id, p.line))
    const candidate = slot?.candidates.find((c) => c.id === (selection.value[slot.id] ?? slot.selected))
    const version = slot?.version || p.line
    const size = candidate?.size || slot?.size || 0
    list.push(
      t('software.steps.download', {
        name: current.name,
        version,
        source: candidate?.name ?? t('software.steps.recommendedSource'),
        size: size > 0 ? formatBytes(size) : t('software.estimate.unknown'),
      }),
    )
    list.push(t('software.steps.unpack', { dir: `/opt/cairn/runtimes/${current.id}/${version}` }))
  } else if (p.packages.length > 0) {
    if (p.method === 'system' && otherSystemSource.value) {
      list.push(t('software.steps.packageSource', { source: otherSystemSource.value }))
    }
    const packages = p.packages.map((pkg) =>
      pkg.version ? t('software.steps.packageVersion', { name: pkg.name, version: pkg.version }) : t('software.steps.packageNewest', { name: pkg.name }),
    )
    let text = t('software.steps.packages', { packages: join(packages) })
    const estimate = result.value?.packages
    if (estimate?.known) {
      text = `${text} ${t('software.steps.packageTotal', { count: estimate.packages, size: formatBytes(estimate.bytes) }, estimate.packages)}`
    }
    list.push(text)
  }
  if (p.libraries?.length) list.push(t('software.steps.libraries', { libraries: join(p.libraries) }))
  if (p.services?.length) list.push(t('software.steps.services', { services: join(p.services) }))
  return list
})

async function submit() {
  const current = entry.value
  const chosen = offer.value
  if (!current || !chosen || !installable(chosen)) return
  submitting.value = true
  error.value = null
  try {
    // Runtime and system-method lines use the chosen source; vendor lines their repository.
    const source = chosenSource(result.value, selection.value, current.id, chosen.line, chosen.method)
    // An ended line is chosen with the warning in view and "Install anyway": that acknowledges it.
    const acceptEndOfLife = chosen.endOfLife
    await store.runJob(() => installSoftware(current.id, chosen.line, { source, acceptEndOfLife }), { rethrow: true })
    open.value = false
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <FormSheet
    v-model:open="open"
    :title="entry ? t('software.install.sheetTitle', { name: entry.name }) : t('software.action.install')"
    :description="entry ? categoryText(entry.category) : undefined"
    :submit-label="offer?.endOfLife ? t('software.install.installAnyway') : t('software.action.install')"
    :submitting="submitting"
    :submit-disabled="!offer || !installable(offer) || (entry !== null && store.runningFor(entry.id) !== null)"
    :steps="steps"
    @submit="submit"
  >
    <template v-if="entry">
      <div class="flex flex-col gap-1.5">
        <span id="install-line-label" class="text-detail font-medium">{{ t('software.install.line') }}</span>
        <OptionGroup v-model="line" :options="lineOptions" aria-labelledby="install-line-label" />
      </div>

      <Notice v-if="offer?.endOfLife" tone="warning">{{ endOfLifeNotice(entry.name, offer) }}</Notice>
      <p v-if="offer && repo && !repo.official" class="m-0 text-xs text-muted-foreground">
        {{ t('software.advanced.unofficialRepo', { repo: repo.name, software: entry.name }) }}
      </p>

      <section class="flex flex-col gap-3" :aria-busy="probing || undefined">
        <div class="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
          <h3 class="m-0 text-detail font-semibold">{{ t('software.install.sources') }}</h3>
          <span v-if="checkedText && !probing" class="text-xs text-muted-foreground">{{ checkedText }}</span>
        </div>
        <div v-if="policy" class="flex flex-wrap items-center gap-x-3 gap-y-1.5">
          <span class="text-xs text-muted-foreground" aria-hidden="true">{{ t('software.install.policyLabel') }}</span>
          <SegmentedControl
            :model-value="policy"
            :options="policyOptions"
            :label="t('software.install.policyLabel')"
            :disabled="!canSetPolicy || savingPolicy"
            @update:model-value="changePolicy"
          />
        </div>
        <Notice v-if="policyError" tone="destructive">{{ errorMessage(policyError) }}</Notice>
        <div v-if="probing" class="flex flex-col gap-2" role="status">
          <span class="text-detail text-muted-foreground">{{ t('software.install.probing') }}</span>
          <Skeleton class="h-14 w-full rounded-md" />
          <Skeleton class="h-14 w-full rounded-md" />
        </div>
        <template v-else-if="result">
          <SourceSlots v-model:selection="selection" :slots="result.slots" :repos="store.repos" :names="names" />
          <DownloadEstimate :result="result" :selection="selection" />
        </template>
        <Notice v-else-if="probeError" tone="warning">
          {{ t('software.install.probeFailed') }} {{ errorMessage(probeError) }}
          <template #action>
            <button type="button" class="underline underline-offset-2 hover:no-underline" @click="probe(true)">
              {{ t('common.action.retry') }}
            </button>
          </template>
        </Notice>
      </section>

      <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
    </template>

    <template #footer-start>
      <Button type="button" variant="ghost" class="me-auto max-md:h-11" :disabled="probing || !offer" @click="probe(true)">
        <RefreshCw aria-hidden="true" />
        {{ t('software.install.checkAgain') }}
      </Button>
    </template>
  </FormSheet>
</template>
