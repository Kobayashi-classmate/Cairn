<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { isApiError } from '@/api/client'
import {
  SNIPPET_COMMAND_MAX,
  SNIPPET_NAME_MAX,
  createSnippet,
  updateSnippet,
  type TermSnippet,
} from '@/api/terminal'
import FormField from '@/components/common/FormField.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { formatBytes } from '@/i18n/format'
import { notifySuccess } from '@/lib/notify'
import { useTerminalStore } from '@/stores/terminal'

// "Save a command" / "Change a command" (form sheet): a name and the text
// that is pasted into the terminal. The limits are the server's: a name of
// up to 64 characters, a command of up to 8 KiB in lines, with tabs but no
// other control characters (they would act on the terminal).
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{ snippet: TermSnippet | null }>()

/** Control characters other than the line feed and the tab (C0, DEL and C1). */
const COMMAND_CONTROL = /[\u0000-\u0008\u000b-\u001f\u007f-\u009f]/
const NAME_CONTROL = /[\u0000-\u001f\u007f-\u009f]/

const { t } = useI18n()
const store = useTerminalStore()
const form = reactive({ name: '', command: '' })
const errors = ref<{ name?: string; command?: string }>({})
const submitting = ref(false)
const error = ref<unknown>(null)

const editing = computed(() => props.snippet !== null)
/** The command as it is stored: "\n" between lines. */
const command = computed(() => form.command.replace(/\r\n/g, '\n'))
const bytes = computed(() => new TextEncoder().encode(command.value).length)
const nameLength = computed(() => [...form.name.trim()].length)

watch(open, (value) => {
  if (!value) return
  form.name = props.snippet?.name ?? ''
  form.command = props.snippet?.command ?? ''
  errors.value = {}
  error.value = null
})

function validate(): boolean {
  const next: { name?: string; command?: string } = {}
  const name = form.name.trim()
  if (!name) next.name = validationMessage('name', 'required')
  else if (nameLength.value > SNIPPET_NAME_MAX) next.name = validationMessage('name', 'max_length', { max: SNIPPET_NAME_MAX })
  else if (NAME_CONTROL.test(name)) next.name = validationMessage('name', 'printable')
  if (!command.value.trim()) next.command = validationMessage('command', 'required')
  else if (bytes.value > SNIPPET_COMMAND_MAX) next.command = t('terminal.snippets.commandTooLong', { size: formatBytes(SNIPPET_COMMAND_MAX) })
  else if (COMMAND_CONTROL.test(command.value)) next.command = t('terminal.snippets.commandControl')
  errors.value = next
  return Object.keys(next).length === 0
}

/** Server errors that belong to one field are shown under it. */
function fieldOf(err: unknown): 'name' | 'command' | null {
  if (!isApiError(err)) return null
  if (err.code === 'terminal.snippet_exists') return 'name'
  if (err.code === 'validation.invalid' && (err.params.field === 'name' || err.params.field === 'command')) return err.params.field
  return null
}

async function submit() {
  if (!validate()) return
  submitting.value = true
  error.value = null
  try {
    const body = { name: form.name.trim(), command: command.value }
    const saved = props.snippet ? await updateSnippet(props.snippet.id, body) : await createSnippet(body)
    store.put(saved)
    notifySuccess(t('terminal.snippets.saved', { name: saved.name }))
    open.value = false
  } catch (err) {
    const field = fieldOf(err)
    if (field) errors.value = { ...errors.value, [field]: errorMessage(err) }
    else error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <FormSheet
    v-model:open="open"
    :title="editing ? t('terminal.snippets.editTitle', { name: snippet?.name ?? '' }) : t('terminal.snippets.addTitle')"
    :description="t('terminal.snippets.sheetIntro')"
    :submit-label="editing ? t('common.action.save') : t('terminal.snippets.add')"
    :submitting="submitting"
    @submit="submit"
  >
    <FormField
      id="snippet-name"
      :label="t('common.field.name')"
      :hint="t('terminal.snippets.nameHint', { count: nameLength, max: SNIPPET_NAME_MAX })"
      :error="errors.name"
    >
      <template #default="field">
        <Input v-bind="field" v-model="form.name" autocomplete="off" dir="auto" />
      </template>
    </FormField>
    <FormField
      id="snippet-command"
      :label="t('common.field.command')"
      :hint="t('terminal.snippets.commandHint', { size: formatBytes(bytes), max: formatBytes(SNIPPET_COMMAND_MAX) })"
      :error="errors.command"
    >
      <template #default="field">
        <Textarea
          v-bind="field"
          v-model="form.command"
          rows="10"
          autocomplete="off"
          autocapitalize="none"
          spellcheck="false"
          wrap="off"
          dir="ltr"
          class="max-h-[50vh] min-h-48 resize-y font-mono text-[13px] leading-relaxed md:text-[13px]"
        />
      </template>
    </FormField>
    <Notice tone="info">{{ t('terminal.snippets.pasteNote') }}</Notice>
    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
