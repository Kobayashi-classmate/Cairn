<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { NAlert, NButton, NCard, NSelect, NSpin, NText } from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import { fetchCerts, setPanelCert, type CertList } from '@/api/certs'
import type { Job } from '@/api/jobs'
import JobProgress from '@/components/JobProgress.vue'
import { errorMessage } from '@/i18n/errors'
import { useSessionStore } from '@/stores/session'

const { t } = useI18n()
const session = useSessionStore()

const list = ref<CertList | null>(null)
const loadError = ref<unknown>(null)
const chosen = ref('')
const saving = ref(false)
const error = ref<unknown>(null)
const jobId = ref<string | null>(null)

const canManage = computed(() => session.can('certs.manage'))

async function load() {
  try {
    list.value = await fetchCerts()
    chosen.value = list.value.panel
    loadError.value = null
  } catch (err) {
    loadError.value = err
  }
}

onMounted(load)

const current = computed(() => list.value?.certs.find((cert) => cert.id === list.value?.panel) ?? null)

/** Only valid, unexpired certificates can be served; "" is the self-signed one. */
const options = computed(() => {
  const now = Date.now() / 1000
  return [
    { value: '', label: t('certs.panel.selfSigned') },
    ...(list.value?.certs ?? [])
      .filter((cert) => cert.state === 'valid' && (!cert.notAfter || cert.notAfter > now))
      .map((cert) => ({ value: cert.id, label: `${cert.name} (${cert.domains.join(', ')})` })),
  ]
})

const chosenDomain = computed(() => list.value?.certs.find((cert) => cert.id === chosen.value)?.domains[0] ?? '')

async function apply() {
  saving.value = true
  error.value = null
  try {
    // Step-up protected: the client asks for the password and retries.
    jobId.value = (await setPanelCert(chosen.value)).id
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    saving.value = false
  }
}

function onFinished(job: Job) {
  if (job.state === 'succeeded') void load()
}
</script>

<template>
  <NCard :title="t('certs.panel.title')" size="small">
    <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>
    <NSpin :show="!list && !loadError">
      <template v-if="list">
        <NText tag="p" class="para">
          {{
            current
              ? t('certs.panel.current', { name: current.name })
              : t('certs.panel.currentSelfSigned')
          }}
          <RouterLink v-if="current" :to="{ name: 'cert', params: { id: current.id } }">{{ t('certs.panel.view') }}</RouterLink>
        </NText>
        <template v-if="canManage">
          <div class="row">
            <NSelect v-model:value="chosen" :options="options" class="select" :disabled="saving" />
            <NButton type="primary" :loading="saving" :disabled="chosen === list.panel" @click="apply">
              {{ t('certs.panel.apply') }}
            </NButton>
          </div>
          <NText v-if="chosen && chosenDomain" depth="3" tag="p" class="hint">
            {{ t('certs.panel.openBy', { domain: chosenDomain }) }}
          </NText>
          <NText depth="3" tag="p" class="hint">{{ t('certs.panel.noRestart') }}</NText>
        </template>
        <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>
        <JobProgress v-if="jobId" :job-id="jobId" class="block" @finished="onFinished" />
      </template>
    </NSpin>
  </NCard>
</template>

<style scoped>
.para {
  margin-block: 0 12px;
}

.row {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.select {
  inline-size: min(420px, 100%);
}

.hint {
  margin-block: 8px 0;
  font-size: 13px;
}

.block {
  margin-block: 12px 0;
}
</style>
