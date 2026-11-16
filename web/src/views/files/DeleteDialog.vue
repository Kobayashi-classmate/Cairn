<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Deletes entries: to the trash, or for good (step-up protected).
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NCheckbox, NModal, NText } from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import { deleteEntries } from '@/api/files'
import type { Job } from '@/api/jobs'
import { baseName } from '@/files/paths'
import { errorMessage } from '@/i18n/errors'

const show = defineModel<boolean>('show', { required: true })
const props = defineProps<{
  root: string
  paths: string[]
  /** Start with "delete permanently" chosen (the trash cannot take them). */
  permanent?: boolean
}>()
const emit = defineEmits<{ started: [job: Job, permanent: boolean] }>()

const { t } = useI18n()
const permanent = ref(false)
const submitting = ref(false)
const error = ref<unknown>(null)

watch(show, (open) => {
  if (!open) return
  permanent.value = !!props.permanent
  error.value = null
})

const title = computed(() =>
  props.paths.length === 1
    ? t('files.delete.titleOne', { name: baseName(props.paths[0] ?? '') })
    : t('files.delete.titleMany', { count: props.paths.length }, props.paths.length),
)

async function submit() {
  submitting.value = true
  error.value = null
  try {
    const forGood = permanent.value
    emit('started', await deleteEntries(props.root, props.paths, forGood), forGood)
    show.value = false
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <NModal v-model:show="show" preset="card" :title="title" class="delete-dialog">
    <NText tag="p" class="intro">{{ permanent ? t('files.delete.permanentText') : t('files.delete.trashText') }}</NText>
    <NCheckbox v-model:checked="permanent">{{ t('files.delete.permanent') }}</NCheckbox>
    <NAlert v-if="permanent" type="warning" class="block">{{ t('files.delete.permanentWarning') }}</NAlert>
    <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>
    <div class="actions">
      <NButton :disabled="submitting" @click="show = false">{{ t('common.action.cancel') }}</NButton>
      <NButton type="error" :loading="submitting" @click="submit">
        {{ permanent ? t('files.delete.submitPermanent') : t('files.delete.submit') }}
      </NButton>
    </div>
  </NModal>
</template>

<style scoped>
.delete-dialog {
  inline-size: min(480px, calc(100vw - 32px));
}

.intro {
  margin-block: 0 12px;
}

.block {
  margin-block-start: 12px;
}

.actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-block-start: 16px;
}
</style>
