<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { isStepUpRequired } from '@/api/client'
import { chownEntries, type FileEntry } from '@/api/files'
import FormField from '@/components/common/FormField.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import SwitchField from '@/components/common/SwitchField.vue'
import { Input } from '@/components/ui/input'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { useFilesStore } from '@/stores/files'
import { entriesLabel } from './actions'

// "Owner" (form sheet, step-up protected, a job): a new owner and group; an
// empty field keeps that one.
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{ root: string; entries: FileEntry[] }>()

/** A user or group name the server accepts. */
const POSIX_NAME = /^[A-Za-z_][A-Za-z0-9_.-]{0,31}\$?$/

const { t } = useI18n()
const store = useFilesStore()
const form = reactive({ user: '', group: '', recursive: false })
const errors = ref<{ user?: string; group?: string }>({})
const submitting = ref(false)
const error = ref<unknown>(null)

const hasDirs = computed(() => props.entries.some((entry) => entry.type === 'dir'))
const name = computed(() => entriesLabel(props.entries.map((entry) => entry.path)))

watch(open, (value) => {
  if (!value) return
  // Prefilled only where every entry agrees; an empty field keeps each entry's own.
  const shared = (pick: (entry: FileEntry) => string | undefined) => {
    const values = new Set(props.entries.map((entry) => pick(entry) ?? ''))
    return values.size === 1 ? ([...values][0] ?? '') : ''
  }
  Object.assign(form, { user: shared((entry) => entry.owner), group: shared((entry) => entry.group), recursive: false })
  errors.value = {}
  error.value = null
})

const owner = computed(() => [form.user.trim(), form.group.trim()].filter(Boolean).join(':'))

const steps = computed(() => {
  if (!form.recursive || !owner.value) return []
  const count = props.entries.length
  return [t('files.chown.step', { owner: owner.value, count }, count)]
})

function validate(): boolean {
  const next: { user?: string; group?: string } = {}
  const user = form.user.trim()
  const group = form.group.trim()
  if (!user && !group) next.user = validationMessage('owner', 'required')
  if (user && !POSIX_NAME.test(user)) next.user = validationMessage('owner', 'posix_name')
  if (group && !POSIX_NAME.test(group)) next.group = validationMessage('group', 'posix_name')
  errors.value = next
  return Object.keys(next).length === 0
}

async function submit() {
  if (!validate()) return
  submitting.value = true
  error.value = null
  try {
    const paths = props.entries.map((entry) => entry.path)
    // Step-up protected: the client asks for the password and retries.
    const job = await store.runJob(
      () => chownEntries(props.root, paths, form.user.trim(), form.group.trim(), form.recursive),
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
    :title="entries.length === 1 ? t('files.chown.title', { name }) : t('files.chown.titleMany', { count: entries.length }, entries.length)"
    :description="t('files.chown.intro')"
    :submit-label="t('files.chown.submit')"
    :submitting="submitting"
    :steps="steps"
    @submit="submit"
  >
    <div class="grid gap-5 sm:grid-cols-2">
      <FormField id="chown-user" :label="t('common.field.owner')" :error="errors.user">
        <template #default="field">
          <Input v-bind="field" v-model="form.user" autocomplete="off" autocapitalize="none" spellcheck="false" class="font-mono" dir="ltr" />
        </template>
      </FormField>
      <FormField id="chown-group" :label="t('common.field.group')" :error="errors.group">
        <template #default="field">
          <Input v-bind="field" v-model="form.group" autocomplete="off" autocapitalize="none" spellcheck="false" class="font-mono" dir="ltr" />
        </template>
      </FormField>
    </div>
    <SwitchField v-if="hasDirs" v-model="form.recursive" :label="t('files.chown.recursive')" />
    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
