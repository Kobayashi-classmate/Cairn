<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { isStepUpRequired } from '@/api/client'
import { fetchUrl } from '@/api/files'
import FormField from '@/components/common/FormField.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import SwitchField from '@/components/common/SwitchField.vue'
import { Input } from '@/components/ui/input'
import { absolutePath, isFileName, joinPath } from '@/files/paths'
import { isHttpUrl } from '@/forms/rules'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { useFilesStore } from '@/stores/files'

// "Download from URL" (form sheet, a job): the server downloads a file into
// the folder shown.
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{ root: string; dir: string }>()

/** Example address (kept out of the template). */
const URL_EXAMPLE = 'https://example.com/file.tar.gz'

const { t } = useI18n()
const store = useFilesStore()
const form = reactive({ url: '', name: '', overwrite: false })
const errors = ref<{ url?: string; name?: string }>({})
const submitting = ref(false)
const error = ref<unknown>(null)

watch(open, (value) => {
  if (!value) return
  Object.assign(form, { url: '', name: '', overwrite: false })
  errors.value = {}
  error.value = null
})

/** Go's path.Base, which the server applies to the URL's path. */
function pathBase(path: string): string {
  if (path === '') return '.'
  const trimmed = path.replace(/\/+$/, '')
  if (trimmed === '') return '/'
  return trimmed.slice(trimmed.lastIndexOf('/') + 1)
}

/**
 * The name the server uses when none is given: the last part of the URL's
 * decoded path, taken as written (the browser's URL parser would resolve
 * "." and ".." segments, the server's does not). "" when that is no usable
 * name, so one has to be typed.
 */
const defaultName = computed(() => {
  const match = /^[a-z][a-z0-9+.-]*:\/\/[^/?#]*([^?#]*)/i.exec(form.url.trim())
  if (!match) return ''
  try {
    const name = pathBase(decodeURIComponent(match[1] ?? ''))
    return isFileName(name) ? name : ''
  } catch {
    return ''
  }
})

const steps = computed(() => {
  const name = form.name.trim() || defaultName.value
  if (!form.url.trim() || !name) return []
  const list = [t('files.fetch.step', { path: absolutePath(props.root, joinPath(props.dir, name)) })]
  if (form.overwrite) list.push(t('files.fetch.stepOverwrite'))
  return list
})

function validate(): boolean {
  const next: { url?: string; name?: string } = {}
  const url = form.url.trim()
  if (!url) next.url = validationMessage('url', 'required')
  else if (!isHttpUrl(url) || url.length > 2048 || /^[a-z]+:\/\/[^/]*@/i.test(url)) next.url = validationMessage('url', 'http_url')
  const name = form.name.trim()
  if (name && !isFileName(name)) next.name = validationMessage('name', 'file_name')
  else if (!name && !next.url && !defaultName.value) next.name = validationMessage('name', 'required')
  errors.value = next
  return Object.keys(next).length === 0
}

async function submit() {
  if (!validate()) return
  submitting.value = true
  error.value = null
  try {
    const job = await store.runJob(
      () => fetchUrl(props.root, props.dir, form.url.trim(), form.name.trim(), form.overwrite),
      { rethrow: true },
    )
    if (job) open.value = false
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
    :title="t('files.fetch.title')"
    :description="t('files.fetch.intro', { dir: absolutePath(root, dir) })"
    :submit-label="t('files.fetch.submit')"
    :submitting="submitting"
    :steps="steps"
    @submit="submit"
  >
    <FormField id="fetch-url" :label="t('common.field.url')" :hint="t('files.fetch.urlHint')" :error="errors.url">
      <template #default="field">
        <Input
          v-bind="field"
          v-model="form.url"
          type="url"
          :placeholder="URL_EXAMPLE"
          autocomplete="off"
          autocapitalize="none"
          spellcheck="false"
          class="font-mono"
          dir="ltr"
        />
      </template>
    </FormField>
    <FormField id="fetch-name" :label="t('common.field.name')" :hint="t('files.fetch.nameHint')" :error="errors.name">
      <template #default="field">
        <Input
          v-bind="field"
          v-model="form.name"
          :placeholder="defaultName || t('files.fetch.namePlaceholder')"
          autocomplete="off"
          autocapitalize="none"
          spellcheck="false"
          dir="auto"
        />
      </template>
    </FormField>
    <SwitchField v-model="form.overwrite" :label="t('files.transfer.overwrite')" />
    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
