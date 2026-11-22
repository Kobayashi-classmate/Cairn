<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { LoaderCircle } from '@lucide/vue'
import { isStepUpRequired } from '@/api/client'
import FormField from '@/components/common/FormField.vue'
import Notice from '@/components/common/Notice.vue'
import { keepOpenOnCompanion, keepOpenOnEscape, trackOverlay } from '@/components/common/overlayGuard'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { isFileName } from '@/files/paths'
import { errorMessage, validationMessage } from '@/i18n/errors'

// Asks for one entry name: new folder, new file, rename. The name without
// its extension starts selected, as file managers do.
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{
  title: string
  /** Where the entry is or goes ("/srv/www/shop"). */
  description: string
  /** Name to start with (renaming). */
  initial?: string
  submitLabel: string
  /** Does the change; the dialog closes when it resolves. */
  action: (name: string) => Promise<void>
}>()

const { t } = useI18n()
trackOverlay(() => open.value)
const name = ref('')
const fieldError = ref<string | null>(null)
const error = ref<unknown>(null)
const submitting = ref(false)
const input = ref<InstanceType<typeof Input> | null>(null)

watch(open, (value) => {
  if (!value) return
  name.value = props.initial ?? ''
  fieldError.value = null
  error.value = null
})

function onOpenAutoFocus(event: Event) {
  event.preventDefault()
  void nextTick(() => {
    const el = (input.value?.$el as HTMLInputElement | undefined) ?? null
    if (!el) return
    el.focus()
    const dot = name.value.lastIndexOf('.')
    if (name.value) el.setSelectionRange(0, dot > 0 ? dot : name.value.length)
  })
}

async function submit() {
  const value = name.value
  if (!value) fieldError.value = validationMessage('name', 'required')
  else if (!isFileName(value)) fieldError.value = validationMessage('name', 'file_name')
  else fieldError.value = null
  if (fieldError.value) return
  submitting.value = true
  error.value = null
  try {
    await props.action(value)
    open.value = false
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent
      class="sm:max-w-[460px]"
      @open-auto-focus="onOpenAutoFocus"
      @interact-outside="keepOpenOnCompanion"
      @escape-key-down="keepOpenOnEscape"
    >
      <form class="flex flex-col gap-4" novalidate @submit.prevent="submit">
        <div class="flex flex-col gap-1.5 pe-6">
          <DialogTitle class="text-title [overflow-wrap:anywhere]">{{ title }}</DialogTitle>
          <DialogDescription class="m-0 font-mono text-xs text-muted-foreground [overflow-wrap:anywhere]" dir="ltr">
            {{ description }}
          </DialogDescription>
        </div>
        <FormField id="entry-name" :label="t('common.field.name')" :error="fieldError">
          <template #default="field">
            <Input
              ref="input"
              v-bind="field"
              v-model="name"
              autocomplete="off"
              autocapitalize="none"
              spellcheck="false"
              dir="auto"
              :disabled="submitting"
            />
          </template>
        </FormField>
        <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
        <div class="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
          <Button type="button" variant="outline" class="max-md:h-11" :disabled="submitting" @click="open = false">
            {{ t('common.action.cancel') }}
          </Button>
          <Button type="submit" class="max-md:h-11" :disabled="submitting">
            <LoaderCircle v-if="submitting" class="animate-spin" aria-hidden="true" />
            {{ submitLabel }}
          </Button>
        </div>
      </form>
    </DialogContent>
  </Dialog>
</template>
