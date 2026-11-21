<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { LoaderCircle } from '@lucide/vue'
import { isStepUpRequired } from '@/api/client'
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { confirmRequest } from '@/composables/useConfirmDialog'
import { errorMessage } from '@/i18n/errors'
import Notice from './Notice.vue'
import { keepOpenOnCompanion, keepOpenOnEscape, trackOverlay } from './overlayGuard'

// Renders the confirmation asked for with confirmAction (one at a time).
const { t } = useI18n()

const open = ref(false)
trackOverlay(() => open.value)
const typed = ref('')
const checked = ref(false)
const running = ref(false)
const error = ref<unknown>(null)

const options = computed(() => confirmRequest.value?.options ?? null)
/** Where focus was before the dialog opened; it returns there afterwards. */
let returnFocus: HTMLElement | null = null

watch(confirmRequest, (request) => {
  if (!request) return
  if (!open.value) returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
  typed.value = ''
  checked.value = request.options.option?.checked === true
  error.value = null
  running.value = false
  open.value = true
})

const description = computed(() => {
  const text = options.value?.description
  return typeof text === 'function' ? text(checked.value) : (text ?? '')
})

const typedOk = computed(() => !options.value?.typeToConfirm || typed.value.trim() === options.value.typeToConfirm)

function finish(confirmed: boolean) {
  const request = confirmRequest.value
  open.value = false
  confirmRequest.value = null
  request?.resolve(confirmed)
  const target = returnFocus
  returnFocus = null
  // The element may be gone (a menu item, a deleted row).
  void nextTick(() => {
    if (target?.isConnected) target.focus()
  })
}

function onOpenChange(value: boolean) {
  // Dismissing while the action runs would hide its outcome.
  if (!value && !running.value) finish(false)
}

async function confirm() {
  const current = options.value
  if (!current || !typedOk.value || running.value) return
  if (!current.run) {
    finish(true)
    return
  }
  running.value = true
  error.value = null
  try {
    await current.run(checked.value)
    running.value = false
    finish(true)
  } catch (err) {
    running.value = false
    if (!isStepUpRequired(err)) error.value = err
  }
}
</script>

<template>
  <AlertDialog :open="open" @update:open="onOpenChange">
    <AlertDialogContent
      v-if="options"
      class="sm:max-w-[460px]"
      @interact-outside="keepOpenOnCompanion"
      @escape-key-down="keepOpenOnEscape"
    >
      <form class="flex flex-col gap-4" novalidate @submit.prevent="confirm">
        <div class="flex flex-col gap-2">
          <AlertDialogTitle class="text-title [overflow-wrap:anywhere]">{{ options.title }}</AlertDialogTitle>
          <AlertDialogDescription class="m-0 text-muted-foreground [overflow-wrap:anywhere]">
            {{ description }}
          </AlertDialogDescription>
        </div>

        <label v-if="options.option" class="flex cursor-pointer items-start gap-2.5 text-detail">
          <Checkbox
            :model-value="checked"
            class="mt-0.5"
            :disabled="running"
            @update:model-value="(value: boolean | 'indeterminate') => (checked = value === true)"
          />
          <span>{{ options.option.label }}</span>
        </label>

        <div v-if="options.typeToConfirm" class="flex flex-col gap-1.5">
          <label for="confirm-typed" class="text-detail font-medium">
            {{ t('common.confirm.typeToConfirm', { text: options.typeToConfirm }) }}
          </label>
          <Input
            id="confirm-typed"
            v-model="typed"
            autocomplete="off"
            autocapitalize="none"
            spellcheck="false"
            dir="ltr"
            class="font-mono"
            :disabled="running"
          />
        </div>

        <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
        <p v-if="running" role="status" class="sr-only">{{ t('common.state.working') }}</p>

        <div class="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
          <Button type="button" variant="outline" class="max-md:h-11" :disabled="running" @click="finish(false)">
            {{ t('common.action.cancel') }}
          </Button>
          <Button
            type="submit"
            :variant="options.destructive ? 'destructive' : 'default'"
            class="max-md:h-11"
            :disabled="!typedOk || running"
          >
            <LoaderCircle v-if="running" class="animate-spin" aria-hidden="true" />
            {{ options.confirmLabel }}
          </Button>
        </div>
      </form>
    </AlertDialogContent>
  </AlertDialog>
</template>
