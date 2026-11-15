<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { NAlert, NButton, NModal, NSpin, NText } from 'naive-ui'
import { isAbortError, isStepUpRequired } from '@/api/client'
import type { Job } from '@/api/jobs'
import {
  fetchSoftwareSettings,
  installSoftware,
  probeSources,
  runtimeSlotId,
  type DownloadPolicy,
  type Offer,
  type ProbeResult,
  type Repository,
  type SoftwareEntry,
} from '@/api/software'
import DownloadEstimate from '@/components/DownloadEstimate.vue'
import SourceSlots from '@/components/SourceSlots.vue'
import { errorMessage } from '@/i18n/errors'
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

/** The download policy, shown before the probe answers (which repeats it). */
const policy = ref<DownloadPolicy | null>(null)

const probing = ref(false)
const result = ref<ProbeResult | null>(null)
const probeError = ref<unknown>(null)
/** Chosen candidate per runtime slot id. */
const selection = ref<Record<string, string>>({})
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
  selection.value = {}
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
    selection.value = Object.fromEntries(
      probed.slots.filter((slot) => slot.selectable && slot.selected).map((slot) => [slot.id, slot.selected!]),
    )
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

const names = computed(() => (props.entry ? { [props.entry.id]: props.entry.name } : {}))

const policyLabel = computed(() => {
  const key = `software.policy.${policy.value}.short`
  return policy.value && te(key) ? t(key) : (policy.value ?? '')
})

async function install() {
  const { entry, offer } = props
  if (!entry || !offer) return
  installing.value = true
  installError.value = null
  try {
    // Only a runtime download can be pointed at a source; other lines use their repositories.
    const source = selection.value[runtimeSlotId(entry.id, offer.line)]
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
      <SourceSlots v-model:selection="selection" :slots="result.slots" :repos="repos" :names="names" />
      <DownloadEstimate :result="result" :selection="selection" class="estimate" />
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

.estimate {
  margin-block-end: 16px;
}

.block {
  margin-block-end: 12px;
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
