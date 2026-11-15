<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NCheckbox, NModal, NText } from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import type { Job } from '@/api/jobs'
import { deleteSite, type SiteView } from '@/api/sites'
import JobProgress from '@/components/JobProgress.vue'
import { errorMessage } from '@/i18n/errors'

const show = defineModel<boolean>('show', { required: true })
const props = defineProps<{ site: SiteView | null }>()
const emit = defineEmits<{ deleted: [] }>()

const { t } = useI18n()

const deleteFiles = ref(false)
const submitting = ref(false)
const error = ref<unknown>(null)
const jobId = ref<string | null>(null)

watch(show, (open) => {
  if (!open) return
  deleteFiles.value = false
  error.value = null
  jobId.value = null
})

async function submit() {
  if (!props.site) return
  submitting.value = true
  error.value = null
  try {
    // Step-up protected: the client asks for the password and retries.
    jobId.value = (await deleteSite(props.site.code, deleteFiles.value)).id
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    submitting.value = false
  }
}

function onFinished(job: Job) {
  if (job.state === 'succeeded') {
    show.value = false
    emit('deleted')
  }
}
</script>

<template>
  <NModal
    v-model:show="show"
    preset="card"
    :title="site ? t('sites.delete.title', { site: site.code }) : ''"
    class="delete-dialog"
  >
    <template v-if="!jobId && site">
      <NText tag="p" class="para">{{ t('sites.delete.text', { site: site.code }) }}</NText>
      <NCheckbox v-model:checked="deleteFiles">{{ t('sites.delete.files') }}</NCheckbox>
      <NText depth="3" tag="p" class="para hint">
        {{
          deleteFiles
            ? t('sites.delete.filesOn', { dir: site.dir, owner: site.owner, runner: site.runner })
            : t('sites.delete.filesOff', { dir: site.dir })
        }}
      </NText>
      <NAlert v-if="error" type="error" class="para">{{ errorMessage(error) }}</NAlert>
      <div class="actions">
        <NButton :disabled="submitting" @click="show = false">{{ t('common.action.cancel') }}</NButton>
        <NButton type="error" :loading="submitting" @click="submit">{{ t('common.action.delete') }}</NButton>
      </div>
    </template>
    <template v-else-if="jobId">
      <JobProgress :job-id="jobId" @finished="onFinished" />
      <div class="actions">
        <NButton @click="show = false">{{ t('sites.create.close') }}</NButton>
      </div>
    </template>
  </NModal>
</template>

<style scoped>
.delete-dialog {
  inline-size: min(520px, calc(100vw - 32px));
}

.para {
  margin-block: 0 12px;
}

.hint {
  margin-block-start: 8px;
  font-size: 13px;
}

.actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-block-start: 16px;
}
</style>
