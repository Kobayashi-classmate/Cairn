<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Has the server download a URL into the current directory.
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NCheckbox, NForm, NFormItem, NInput, NModal, NText, type FormInst, type FormRules } from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import { fetchUrl } from '@/api/files'
import type { Job } from '@/api/jobs'
import { absolutePath, isFileName } from '@/files/paths'
import { checkRule, isHttpUrl, requiredRule } from '@/forms/rules'
import { errorMessage } from '@/i18n/errors'

const show = defineModel<boolean>('show', { required: true })
const props = defineProps<{ root: string; dir: string }>()
const emit = defineEmits<{ started: [job: Job] }>()

/** Example address (kept out of the template; see the type-check note in SiteForm). */
const URL_EXAMPLE = 'https://example.com/file.tar.gz'

const { t } = useI18n()
const formRef = ref<FormInst | null>(null)
const form = reactive({ url: '', name: '', overwrite: false })
const submitting = ref(false)
const error = ref<unknown>(null)

watch(show, (open) => {
  if (!open) return
  Object.assign(form, { url: '', name: '', overwrite: false })
  error.value = null
})

/** The name the server uses when none is given: the URL's last part. */
const defaultName = computed(() => {
  try {
    const path = new URL(form.url.trim()).pathname
    return decodeURIComponent(path.slice(path.lastIndexOf('/') + 1))
  } catch {
    return ''
  }
})

const rules = computed<FormRules>(() => ({
  url: [
    requiredRule('url'),
    checkRule('url', 'http_url', (v) => isHttpUrl(v.trim()) && v.trim().length <= 2048 && !/^[a-z]+:\/\/[^/]*@/i.test(v.trim())),
  ],
  name: [checkRule('name', 'file_name', isFileName)],
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
    emit('started', await fetchUrl(props.root, props.dir, form.url.trim(), form.name.trim(), form.overwrite))
    show.value = false
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <NModal v-model:show="show" preset="card" :title="t('files.fetch.title')" class="fetch-dialog">
    <NText depth="3" tag="p" class="intro">{{ t('files.fetch.intro', { dir: absolutePath(root, dir) }) }}</NText>
    <NForm ref="formRef" :model="form" :rules="rules" :disabled="submitting">
      <NFormItem path="url" :label="t('common.field.url')">
        <NInput v-model:value="form.url" :placeholder="URL_EXAMPLE" :input-props="{ type: 'url', autocomplete: 'off', spellcheck: false }" />
      </NFormItem>
      <NFormItem path="name" :label="t('common.field.name')">
        <NInput
          v-model:value="form.name"
          :placeholder="defaultName || t('files.fetch.namePlaceholder')"
          :input-props="{ autocomplete: 'off', spellcheck: false }"
        />
      </NFormItem>
    </NForm>
    <NCheckbox v-model:checked="form.overwrite">{{ t('files.transfer.overwrite') }}</NCheckbox>
    <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>
    <div class="actions">
      <NButton :disabled="submitting" @click="show = false">{{ t('common.action.cancel') }}</NButton>
      <NButton type="primary" :loading="submitting" @click="submit">{{ t('files.fetch.submit') }}</NButton>
    </div>
  </NModal>
</template>

<style scoped>
.fetch-dialog {
  inline-size: min(520px, calc(100vw - 32px));
}

.intro {
  margin-block: 0 12px;
  font-size: 13px;
  overflow-wrap: anywhere;
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
