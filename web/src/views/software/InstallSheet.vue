<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RefreshCw } from '@lucide/vue'
import { isAbortError, isStepUpRequired } from '@/api/client'
import {
  fetchSoftwareSettings,
  installSoftware,
  isAvailable,
  probeSources,
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
import type { OptionItem } from '@/components/common/types'
import SourceSlots from '@/components/SourceSlots.vue'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { errorMessage } from '@/i18n/errors'
import { formatBytes, formatIsoDate } from '@/i18n/format'
import { categoryText, lineFacts } from '@/software/display'
import { useSoftwareStore } from '@/stores/software'

// "Install <software>" (interface spec, section 6: form sheet with "Will
// do"): pick the line, see where it would be downloaded from (measured, the
// recommended source preselected) and the exact steps, then install as a
// job. Changing the line probes again; a probe in flight is abandoned.
const { t, te } = useI18n()
const store = useSoftwareStore()

const open = computed({
  get: () => store.installOpen,
  set: (value: boolean) => (store.installOpen = value),
})

const entry = computed(() => (store.installTarget ? store.entry(store.installTarget.software) : null))
const line = ref('')
const offer = computed<Offer | null>(() => entry.value?.offers.find((o) => o.line === line.value) ?? null)

/** The download policy, shown before the probe answers (which repeats it). */
const policy = ref<DownloadPolicy | null>(null)
const probing = ref(false)
const result = ref<ProbeResult | null>(null)
const probeError = ref<unknown>(null)
/** Chosen candidate per runtime slot id. */
const selection = ref<Record<string, string>>({})
const submitting = ref(false)
const error = ref<unknown>(null)
let controller: AbortController | null = null
/** The line the last probe was started for. */
let probedLine = ''

function installable(o: Offer): boolean {
  return isAvailable(o) && !o.installed
}

function firstLine(): string {
  const offers = entry.value?.offers ?? []
  const wanted = store.installTarget?.line
  const pick =
    offers.find((o) => o.line === wanted && installable(o)) ??
    offers.find((o) => o.recommended && installable(o)) ??
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
    void loadPolicy()
    line.value = firstLine()
    void probe()
  },
  { immediate: true },
)

watch(line, (value) => {
  if (open.value && value !== probedLine) void probe()
})

onBeforeUnmount(() => controller?.abort())

async function loadPolicy() {
  try {
    policy.value = (await fetchSoftwareSettings()).downloadPolicy
  } catch {
    // The probe result names the policy as well.
  }
}

/** Checks the download sources of the chosen line; takes a few seconds, up to about 45. */
async function probe() {
  controller?.abort()
  probedLine = line.value
  result.value = null
  probeError.value = null
  selection.value = {}
  const current = entry.value
  if (!current || !line.value) {
    probing.value = false
    return
  }
  const request = new AbortController()
  controller = request
  probing.value = true
  try {
    const probed = await probeSources([{ software: current.id, line: line.value }], { signal: request.signal })
    result.value = probed
    policy.value = probed.policy
    selection.value = Object.fromEntries(
      probed.slots.filter((slot) => slot.selectable && slot.selected).map((slot) => [slot.id, slot.selected!]),
    )
  } catch (err) {
    if (isAbortError(err)) return
    probeError.value = err
  } finally {
    if (controller === request) {
      controller = null
      probing.value = false
    }
  }
}

const lineOptions = computed<OptionItem[]>(() =>
  (entry.value?.offers ?? []).map((o) => ({
    value: o.line,
    label: `${entry.value?.name ?? ''} ${o.line}`,
    hint: lineFacts(o),
    disabled: !installable(o),
  })),
)

const names = computed(() => (entry.value ? { [entry.value.id]: entry.value.name } : {}))

const policyLabel = computed(() => {
  const key = `software.policy.${policy.value}.short`
  return policy.value && te(key) ? t(key) : (policy.value ?? '')
})

const repo = computed(() => (offer.value?.repo ? (store.repos.get(offer.value.repo) ?? null) : null))

/** The plan of the chosen line, from the probe. */
const plan = computed<LinePlan | null>(
  () => result.value?.plan?.find((p) => p.software === entry.value?.id && p.line === line.value) ?? null,
)

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
    // Only a runtime download can be pointed at a source; other lines use their repositories.
    const source = selection.value[runtimeSlotId(current.id, chosen.line)]
    await store.runJob(() => installSoftware(current.id, chosen.line, source), { rethrow: true })
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
    :submit-label="t('software.action.install')"
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

      <Notice v-if="offer?.endOfLife && offer.eol" tone="warning">
        {{ t('software.install.endOfLife', { line: offer.line, date: formatIsoDate(offer.eol) }) }}
      </Notice>
      <p v-if="offer && repo && !repo.official" class="m-0 text-xs text-muted-foreground">
        {{ t('software.advanced.unofficialRepo', { repo: repo.name, software: entry.name }) }}
      </p>

      <section class="flex flex-col gap-3" :aria-busy="probing || undefined">
        <div class="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
          <h3 class="m-0 text-detail font-semibold">{{ t('software.install.sources') }}</h3>
          <span v-if="policyLabel" class="text-xs text-muted-foreground">{{ t('software.install.policy', { policy: policyLabel }) }}</span>
        </div>
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
            <button type="button" class="underline underline-offset-2 hover:no-underline" @click="probe">
              {{ t('common.action.retry') }}
            </button>
          </template>
        </Notice>
      </section>

      <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
    </template>

    <template #footer-start>
      <Button type="button" variant="ghost" class="me-auto max-md:h-11" :disabled="probing || !offer" @click="probe">
        <RefreshCw aria-hidden="true" />
        {{ t('software.install.checkAgain') }}
      </Button>
    </template>
  </FormSheet>
</template>
