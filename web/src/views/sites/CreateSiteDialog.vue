<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { NAlert, NButton, NModal } from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import type { Job } from '@/api/jobs'
import { createSite, type RewriteRule } from '@/api/sites'
import JobProgress from '@/components/JobProgress.vue'
import { errorMessage } from '@/i18n/errors'
import SiteForm from './SiteForm.vue'

const show = defineModel<boolean>('show', { required: true })
defineProps<{ phpLines: string[]; rewrites: RewriteRule[] }>()
const emit = defineEmits<{ changed: [] }>()

const { t } = useI18n()
const router = useRouter()

const formRef = ref<InstanceType<typeof SiteForm> | null>(null)
const submitting = ref(false)
const error = ref<unknown>(null)
/** The creation job, followed inline once started. */
const jobId = ref<string | null>(null)
const finished = ref<Job | null>(null)

watch(show, (open) => {
  if (!open) return
  error.value = null
  jobId.value = null
  finished.value = null
  formRef.value?.reset()
})

async function submit() {
  const request = await formRef.value?.collect()
  if (!request) return
  submitting.value = true
  error.value = null
  try {
    jobId.value = (await createSite(request)).id
    emit('changed')
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    submitting.value = false
  }
}

async function onFinished(job: Job) {
  finished.value = job
  emit('changed')
  const code = job.params.site
  // A created site opens right away; a failed one stays here with the error.
  if (job.state === 'succeeded' && typeof code === 'string') {
    show.value = false
    await router.push({ name: 'site', params: { code } })
  }
}

async function openSite() {
  const code = finished.value?.params.site
  show.value = false
  if (typeof code === 'string') await router.push({ name: 'site', params: { code } })
}
</script>

<template>
  <NModal v-model:show="show" preset="card" :title="t('sites.create.title')" class="site-dialog">
    <template v-if="!jobId">
      <SiteForm ref="formRef" mode="create" :php-lines="phpLines" :rewrites="rewrites" :disabled="submitting" />
      <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>
      <div class="actions">
        <NButton :disabled="submitting" @click="show = false">{{ t('common.action.cancel') }}</NButton>
        <NButton type="primary" :loading="submitting" @click="submit">{{ t('sites.create.submit') }}</NButton>
      </div>
    </template>
    <template v-else>
      <JobProgress :job-id="jobId" @finished="onFinished" />
      <div class="actions">
        <NButton v-if="finished && finished.state !== 'succeeded'" @click="openSite">
          {{ t('sites.create.openSite') }}
        </NButton>
        <NButton @click="show = false">{{ t('sites.create.close') }}</NButton>
      </div>
    </template>
  </NModal>
</template>

<style scoped>
.site-dialog {
  inline-size: min(680px, calc(100vw - 32px));
}

.block {
  margin-block-end: 12px;
}

.actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-block-start: 16px;
}
</style>
