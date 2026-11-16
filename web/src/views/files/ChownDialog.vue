<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Changes owner and group (step-up protected); an empty field keeps it.
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NForm, NFormItem, NInput, NModal, NSwitch, type FormInst, type FormRules } from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import { chownEntries, type FileEntry } from '@/api/files'
import type { Job } from '@/api/jobs'
import { checkRule } from '@/forms/rules'
import { errorMessage, validationMessage } from '@/i18n/errors'

const show = defineModel<boolean>('show', { required: true })
const props = defineProps<{ root: string; entries: FileEntry[] }>()
const emit = defineEmits<{ started: [job: Job] }>()

/** A user or group name the server accepts. */
const POSIX_NAME = /^[A-Za-z_][A-Za-z0-9_.-]{0,31}\$?$/

const { t } = useI18n()
const formRef = ref<FormInst | null>(null)
const form = reactive({ user: '', group: '', recursive: false })
const submitting = ref(false)
const error = ref<unknown>(null)
/** Neither owner nor group was given. */
const emptyError = ref(false)

const hasDirs = computed(() => props.entries.some((entry) => entry.type === 'dir'))

watch(show, (open) => {
  if (!open) return
  const first = props.entries[0]
  Object.assign(form, { user: first?.owner ?? '', group: first?.group ?? '', recursive: false })
  error.value = null
  emptyError.value = false
})

const rules = computed<FormRules>(() => ({
  user: [checkRule('owner', 'posix_name', (v) => POSIX_NAME.test(v))],
  group: [checkRule('group', 'posix_name', (v) => POSIX_NAME.test(v))],
}))

const title = computed(() =>
  props.entries.length === 1
    ? t('files.chown.titleOne', { name: props.entries[0]?.name ?? '' })
    : t('files.chown.titleMany', { count: props.entries.length }, props.entries.length),
)

async function submit() {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  if (!form.user.trim() && !form.group.trim()) {
    error.value = null
    emptyError.value = true
    return
  }
  emptyError.value = false
  submitting.value = true
  error.value = null
  try {
    const paths = props.entries.map((entry) => entry.path)
    emit('started', await chownEntries(props.root, paths, form.user.trim(), form.group.trim(), form.recursive))
    show.value = false
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <NModal v-model:show="show" preset="card" :title="title" class="chown-dialog">
    <NForm ref="formRef" :model="form" :rules="rules" :disabled="submitting">
      <NFormItem path="user" :label="t('common.field.owner')">
        <NInput v-model:value="form.user" class="mono" :input-props="{ autocomplete: 'off', spellcheck: false }" />
      </NFormItem>
      <NFormItem path="group" :label="t('common.field.group')">
        <NInput v-model:value="form.group" class="mono" :input-props="{ autocomplete: 'off', spellcheck: false }" />
      </NFormItem>
    </NForm>
    <div v-if="hasDirs" class="option">
      <NSwitch v-model:value="form.recursive" />
      <span>{{ t('files.chown.recursive') }}</span>
    </div>
    <NAlert v-if="emptyError" type="warning" class="block">{{ validationMessage('owner', 'required') }}</NAlert>
    <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>
    <div class="actions">
      <NButton :disabled="submitting" @click="show = false">{{ t('common.action.cancel') }}</NButton>
      <NButton type="primary" :loading="submitting" @click="submit">{{ t('common.action.save') }}</NButton>
    </div>
  </NModal>
</template>

<style scoped>
.chown-dialog {
  inline-size: min(460px, calc(100vw - 32px));
}

.option {
  display: flex;
  align-items: center;
  gap: 8px;
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

.mono :deep(input) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}
</style>
