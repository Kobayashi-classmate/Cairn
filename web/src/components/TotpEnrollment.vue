<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NForm,
  NFormItem,
  NInput,
  NSpin,
  NText,
  useMessage,
  type FormInst,
  type FormRules,
} from 'naive-ui'
import { beginTotp, confirmTotp, type TotpEnrollment } from '@/api/account'
import { isApiError, isStepUpRequired } from '@/api/client'
import RecoveryCodes from '@/components/RecoveryCodes.vue'
import TotpQrCode from '@/components/TotpQrCode.vue'
import { totpCodeRules } from '@/forms/rules'
import { errorMessage } from '@/i18n/errors'
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
const message = useMessage()
const session = useSessionStore()

const stage = ref<'starting' | 'enrolling' | 'codes' | 'failed'>('starting')
const enrollment = ref<TotpEnrollment | null>(null)
const recoveryCodes = ref<string[]>([])
const busy = ref(false)
const error = ref<unknown>(null)

const formRef = ref<FormInst | null>(null)
const form = reactive({ code: '' })
const rules: FormRules = { code: totpCodeRules() }

/** The secret in groups of four characters, easier to type. */
const groupedSecret = computed(() => enrollment.value?.secret.match(/.{1,4}/g)?.join(' ') ?? '')

async function begin() {
  stage.value = 'starting'
  error.value = null
  try {
    enrollment.value = await beginTotp()
    form.code = ''
    stage.value = 'enrolling'
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
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  error.value = null
  busy.value = true
  try {
    recoveryCodes.value = (await confirmTotp(form.code)).recoveryCodes
    session.setTotpEnabled(true)
    enrollment.value = null
    stage.value = 'codes'
    message.success(t('settings.security.enabled'))
  } catch (err) {
    error.value = err
    // The pending secret expired: offer to start over.
    if (isApiError(err) && err.code === 'auth.totp_not_pending') stage.value = 'failed'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="enroll">
    <NAlert v-if="error" type="error">{{ errorMessage(error) }}</NAlert>

    <div v-if="stage === 'starting'" class="enroll__starting"><NSpin size="small" /></div>

    <div v-else-if="stage === 'failed'" class="actions">
      <NButton type="primary" @click="begin">{{ t('settings.security.enroll.restart') }}</NButton>
      <NButton @click="emit('cancel')">{{ t('common.action.cancel') }}</NButton>
    </div>

    <template v-else-if="stage === 'enrolling' && enrollment">
      <div>
        <NText tag="p" class="intro">{{ t('settings.security.enroll.scan') }}</NText>
        <TotpQrCode :uri="enrollment.uri" :label="t('settings.security.enroll.qrLabel')" />
      </div>
      <div>
        <NText depth="3" tag="p" class="intro">{{ t('settings.security.enroll.manual') }}</NText>
        <code class="secret" dir="ltr">{{ groupedSecret }}</code>
      </div>
      <NForm
        ref="formRef"
        :model="form"
        :rules="rules"
        :disabled="busy"
        class="enroll__form"
        @submit.prevent="confirm"
      >
        <NText tag="p" class="intro">{{ t('settings.security.enroll.enterCode') }}</NText>
        <NFormItem path="code" :label="t('common.field.code')">
          <NInput
            v-model:value="form.code"
            :maxlength="6"
            :input-props="{ autocomplete: 'one-time-code', inputmode: 'numeric' }"
          />
        </NFormItem>
        <div class="actions">
          <NButton type="primary" attr-type="submit" :loading="busy">
            {{ t('settings.security.enroll.submit') }}
          </NButton>
          <NButton :disabled="busy" @click="emit('cancel')">{{ t('common.action.cancel') }}</NButton>
        </div>
      </NForm>
    </template>

    <RecoveryCodes v-else-if="stage === 'codes'" :codes="recoveryCodes" @done="emit('done')" />
  </div>
</template>

<style scoped>
.enroll {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.enroll__starting {
  padding-block: 12px;
}

.enroll__form {
  max-inline-size: 320px;
}

.intro {
  margin-block: 0 12px;
}

.actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.secret {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 15px;
  letter-spacing: 0.05em;
  overflow-wrap: anywhere;
  user-select: all;
}
</style>
