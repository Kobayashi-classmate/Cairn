<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Copy, LoaderCircle } from '@lucide/vue'
import { beginTotp, confirmTotp, type TotpEnrollment } from '@/api/account'
import { isApiError, isStepUpRequired } from '@/api/client'
import FormField from '@/components/common/FormField.vue'
import Notice from '@/components/common/Notice.vue'
import OtpInput from '@/components/common/OtpInput.vue'
import RecoveryCodes from '@/components/RecoveryCodes.vue'
import TotpQrCode from '@/components/TotpQrCode.vue'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { TOTP_CODE_PATTERN } from '@/forms/rules'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { copyText, notifySuccess } from '@/lib/notify'
import { useSessionStore } from '@/stores/session'

/**
 * Turning on two-step verification: starts enrollment when mounted (which
 * may ask to re-authenticate), shows the QR code and secret, confirms a code
 * from the app and shows the recovery codes once. Used by the security
 * settings and the first-login wizard.
 */
const emit = defineEmits<{
  /** Enabled, and the user has confirmed saving the recovery codes. */
  done: []
  /** The user gave up (or declined to re-authenticate). */
  cancel: []
}>()

const { t } = useI18n()
const session = useSessionStore()

const stage = ref<'starting' | 'enrolling' | 'codes' | 'failed'>('starting')
const enrollment = ref<TotpEnrollment | null>(null)
const recoveryCodes = ref<string[]>([])
const code = ref('')
const codeError = ref<string | null>(null)
const busy = ref(false)
const error = ref<unknown>(null)
const otp = ref<InstanceType<typeof OtpInput> | null>(null)

/** The secret in groups of four characters, easier to type. */
const groupedSecret = computed(() => enrollment.value?.secret.match(/.{1,4}/g)?.join(' ') ?? '')

async function begin() {
  stage.value = 'starting'
  error.value = null
  try {
    enrollment.value = await beginTotp()
    code.value = ''
    codeError.value = null
    stage.value = 'enrolling'
    void nextTick(() => otp.value?.focus())
  } catch (err) {
    if (isStepUpRequired(err)) {
      emit('cancel')
      return
    }
    error.value = err
    stage.value = 'failed'
  }
}

onMounted(begin)

async function confirm() {
  if (busy.value) return
  if (!code.value) {
    codeError.value = validationMessage('code', 'required')
    return
  }
  if (!TOTP_CODE_PATTERN.test(code.value)) {
    codeError.value = validationMessage('code', 'pattern')
    return
  }
  codeError.value = null
  error.value = null
  busy.value = true
  try {
    recoveryCodes.value = (await confirmTotp(code.value)).recoveryCodes
    session.setTotpEnabled(true)
    enrollment.value = null
    stage.value = 'codes'
    notifySuccess(t('settings.security.enabled'))
  } catch (err) {
    // The pending secret expired: offer to start over.
    if (isApiError(err) && err.code === 'auth.totp_not_pending') {
      error.value = err
      stage.value = 'failed'
    } else if (isApiError(err) && err.code === 'auth.mfa_invalid_code') {
      codeError.value = errorMessage(err)
      code.value = ''
      void nextTick(() => otp.value?.focus())
    } else {
      error.value = err
    }
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="flex flex-col gap-5">
    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>

    <div v-if="stage === 'starting'" role="status" class="flex flex-col gap-3">
      <span class="sr-only">{{ t('common.state.loading') }}</span>
      <Skeleton class="h-4 w-64 max-w-full" />
      <Skeleton class="size-48 rounded-md" />
    </div>

    <div v-else-if="stage === 'failed'" class="flex flex-wrap gap-2">
      <Button class="max-md:h-11" @click="begin">{{ t('settings.security.enroll.restart') }}</Button>
      <Button variant="outline" class="max-md:h-11" @click="emit('cancel')">{{ t('common.action.cancel') }}</Button>
    </div>

    <template v-else-if="stage === 'enrolling' && enrollment">
      <div class="grid gap-5 sm:grid-cols-[auto_minmax(0,1fr)] sm:items-start">
        <div class="flex flex-col gap-2">
          <p class="m-0 font-medium">{{ t('settings.security.enroll.scan') }}</p>
          <div class="w-fit rounded-md border bg-card p-1">
            <TotpQrCode :uri="enrollment.uri" :label="t('settings.security.enroll.qrLabel')" />
          </div>
        </div>
        <div class="flex min-w-0 flex-col gap-2">
          <p class="m-0 text-muted-foreground">{{ t('settings.security.enroll.manual') }}</p>
          <div class="flex min-w-0 items-center gap-2">
            <code class="min-w-0 flex-1 rounded-md bg-muted px-3 py-2 font-mono text-[15px] tracking-wide [overflow-wrap:anywhere] select-all" dir="ltr">
              {{ groupedSecret }}
            </code>
            <Button
              variant="outline"
              size="icon"
              class="max-md:size-11"
              :aria-label="t('settings.security.enroll.copyKey')"
              :title="t('settings.security.enroll.copyKey')"
              @click="copyText(enrollment.secret)"
            >
              <Copy aria-hidden="true" />
            </Button>
          </div>
        </div>
      </div>

      <form class="flex max-w-sm flex-col gap-3" novalidate @submit.prevent="confirm">
        <p class="m-0 font-medium">{{ t('settings.security.enroll.enterCode') }}</p>
        <FormField id="totp-enroll-code" :label="t('common.field.code')" :error="codeError">
          <template #default="field">
            <OtpInput
              :id="field.id"
              ref="otp"
              v-model="code"
              :described-by="field['aria-describedby']"
              :invalid="field['aria-invalid']"
              :disabled="busy"
              @complete="confirm"
            />
          </template>
        </FormField>
        <div class="flex flex-wrap gap-2">
          <Button type="submit" class="max-md:h-11" :disabled="busy">
            <LoaderCircle v-if="busy" class="animate-spin" aria-hidden="true" />
            {{ t('settings.security.enroll.submit') }}
          </Button>
          <Button type="button" variant="outline" class="max-md:h-11" :disabled="busy" @click="emit('cancel')">
            {{ t('common.action.cancel') }}
          </Button>
        </div>
      </form>
    </template>

    <RecoveryCodes v-else-if="stage === 'codes'" :codes="recoveryCodes" @done="emit('done')" />
  </div>
</template>
