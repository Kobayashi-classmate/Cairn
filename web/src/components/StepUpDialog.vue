<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { LoaderCircle } from '@lucide/vue'
import { isApiError } from '@/api/client'
import { finishStepUp, stepUpOpen } from '@/auth/stepUp'
import { focusReturnTarget, returnFocus } from '@/components/common/focusReturn'
import FormField from '@/components/common/FormField.vue'
import Notice from '@/components/common/Notice.vue'
import OtpInput from '@/components/common/OtpInput.vue'
import PasswordInput from '@/components/common/PasswordInput.vue'
import { trackOverlay } from '@/components/common/overlayGuard'
import { AlertDialog, AlertDialogContent, AlertDialogDescription, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { TOTP_CODE_PATTERN } from '@/forms/rules'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { useSessionStore } from '@/stores/session'

// Re-authentication before a sensitive request (auth/stepUp.ts): the
// password, and the one-time code when two-step sign-in is on. It opens on
// top of whatever sheet, drawer or dialog sent the request; Reka keeps the
// layers in order (Escape and outside clicks reach this one only), and the
// layers below stay open meanwhile (components/common/overlayGuard.ts).
// Clicking outside does not cancel: the requests wait for an answer.

const { t } = useI18n()
const session = useSessionStore()
trackOverlay(() => stepUpOpen.value)

const password = ref('')
const code = ref('')
const askCode = ref(false)
const submitting = ref(false)
const error = ref<unknown>(null)
const errors = ref<{ password?: string; code?: string }>({})
const passwordInput = ref<{ focus: () => void } | null>(null)
/** Where focus was when the request asked for re-authentication. */
let returnTo: HTMLElement | null = null

// Immediate: the component may be loaded only when the first request arrives.
watch(
  stepUpOpen,
  (open) => {
    if (!open) return
    returnTo = focusReturnTarget()
    password.value = ''
    code.value = ''
    error.value = null
    errors.value = {}
    askCode.value = session.user?.totpEnabled === true
  },
  { immediate: true },
)

// Pending requests cannot be retried once the session is gone.
watch(
  () => session.user,
  (user) => {
    if (!user && stepUpOpen.value) finishStepUp(false)
  },
)

const errorText = computed(() => (error.value ? errorMessage(error.value) : ''))

function validate(): boolean {
  const next: typeof errors.value = {}
  if (!password.value) next.password = validationMessage('password', 'required')
  if (askCode.value && !TOTP_CODE_PATTERN.test(code.value)) {
    next.code = code.value ? validationMessage('code', 'pattern') : validationMessage('code', 'required')
  }
  errors.value = next
  return Object.keys(next).length === 0
}

async function submit() {
  if (submitting.value || !validate()) return
  error.value = null
  submitting.value = true
  try {
    await session.elevate({ password: password.value, code: askCode.value ? code.value : undefined })
    finishStepUp(true)
  } catch (err) {
    // The server wants a code even though the profile said 2FA was off.
    if (isApiError(err) && err.code === 'auth.mfa_required') askCode.value = true
    error.value = err
  } finally {
    submitting.value = false
  }
}

function onOpenChange(open: boolean) {
  if (!open && !submitting.value) finishStepUp(false)
}

/** Back to where the user was; a menu item that opened the request has closed by now. */
function onCloseAutoFocus(event: Event) {
  const target = returnTo
  returnTo = null
  if (returnFocus(target)) event.preventDefault()
}

/** Start in the password field rather than on the first button. */
function onOpenAutoFocus(event: Event) {
  event.preventDefault()
  void nextTick(() => passwordInput.value?.focus())
}
</script>

<template>
  <AlertDialog :open="stepUpOpen" @update:open="onOpenChange">
    <AlertDialogContent class="sm:max-w-[420px]" @open-auto-focus="onOpenAutoFocus" @close-auto-focus="onCloseAutoFocus">
      <form class="flex flex-col gap-4" novalidate @submit.prevent="submit">
        <div class="flex flex-col gap-2">
          <AlertDialogTitle class="text-title">{{ t('auth.stepUp.title') }}</AlertDialogTitle>
          <AlertDialogDescription class="m-0 text-muted-foreground">{{ t('auth.stepUp.intro') }}</AlertDialogDescription>
        </div>
        <Notice v-if="errorText" tone="destructive" role="alert">{{ errorText }}</Notice>
        <FormField id="step-up-password" :label="t('common.field.password')" :error="errors.password">
          <template #default="field">
            <PasswordInput
              ref="passwordInput"
              v-model="password"
              v-bind="field"
              autocomplete="current-password"
              :disabled="submitting"
            />
          </template>
        </FormField>
        <FormField v-if="askCode" id="step-up-code" :label="t('common.field.code')" :error="errors.code">
          <template #default="field">
            <OtpInput
              v-model="code"
              :id="field.id"
              :described-by="field['aria-describedby']"
              :invalid="!!errors.code"
              :disabled="submitting"
              @complete="submit"
            />
          </template>
        </FormField>
        <div class="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
          <Button type="button" variant="outline" class="max-md:h-11" :disabled="submitting" @click="finishStepUp(false)">
            {{ t('common.action.cancel') }}
          </Button>
          <Button type="submit" class="max-md:h-11" :disabled="submitting">
            <LoaderCircle v-if="submitting" class="animate-spin" aria-hidden="true" />
            {{ t('common.action.confirm') }}
          </Button>
        </div>
      </form>
    </AlertDialogContent>
  </AlertDialog>
</template>
