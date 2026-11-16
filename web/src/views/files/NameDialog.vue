<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Asks for one entry name: new folder, new file, rename.
import { computed, nextTick, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NForm, NFormItem, NInput, NModal, type FormInst, type FormRules, type InputInst } from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import { isFileName } from '@/files/paths'
import { checkRule, requiredRule } from '@/forms/rules'
import { errorMessage } from '@/i18n/errors'

const show = defineModel<boolean>('show', { required: true })
const props = defineProps<{
  title: string
  /** Name to start with (renaming). */
  initial?: string
  submitText: string
  /** Does the change; the dialog closes when it resolves. */
  action: (name: string) => Promise<void>
}>()

const { t } = useI18n()
const formRef = ref<FormInst | null>(null)
const inputRef = ref<InputInst | null>(null)
const form = reactive({ name: '' })
const submitting = ref(false)
const error = ref<unknown>(null)

watch(show, async (open) => {
  if (!open) return
  form.name = props.initial ?? ''
  error.value = null
  await nextTick()
  inputRef.value?.focus()
  // Select the name without its extension, like file managers do.
  const input = inputRef.value?.inputElRef
  const dot = form.name.lastIndexOf('.')
  if (input && form.name) input.setSelectionRange(0, dot > 0 ? dot : form.name.length)
})

const rules = computed<FormRules>(() => ({
  name: [requiredRule('name'), checkRule('name', 'file_name', isFileName)],
}))

async function submit() {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  submitting.value = true
  error.value = null
  try {
    await props.action(form.name)
    show.value = false
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <NModal v-model:show="show" preset="card" :title="title" class="name-dialog">
    <NForm ref="formRef" :model="form" :rules="rules" :disabled="submitting" @submit.prevent="submit">
      <NFormItem path="name" :label="t('common.field.name')">
        <NInput
          ref="inputRef"
          v-model:value="form.name"
          :input-props="{ autocomplete: 'off', spellcheck: false }"
          @keydown.enter.prevent="submit"
        />
      </NFormItem>
    </NForm>
    <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>
    <div class="actions">
      <NButton :disabled="submitting" @click="show = false">{{ t('common.action.cancel') }}</NButton>
      <NButton type="primary" :loading="submitting" @click="submit">{{ submitText }}</NButton>
    </div>
  </NModal>
</template>

<style scoped>
.name-dialog {
  inline-size: min(460px, calc(100vw - 32px));
}

.block {
  margin-block-end: 12px;
}

.actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
