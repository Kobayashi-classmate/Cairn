<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NCard,
  NForm,
  NFormItem,
  NInput,
  NTag,
  NText,
  useMessage,
  type FormInst,
  type FormRules,
} from 'naive-ui'
import {
  beginTotp,
  confirmTotp,
  disableTotp,
  regenerateRecoveryCodes,
  type TotpEnrollment,
} from '@/api/account'
import { isApiError, isStepUpRequired } from '@/api/client'
import { totpCodeRules } from '@/auth/rules'
import RecoveryCodes from '@/components/RecoveryCodes.vue'
import TotpQrCode from '@/components/TotpQrCode.vue'
import { useConfirm } from '@/composables/useConfirm'
import { errorMessage } from '@/i18n/errors'
import { useSessionStore } from '@/stores/session'

const { t } = useI18n()
const message = useMessage()
const confirm = useConfirm()
const session = useSessionStore()

/**
 * idle: status and actions; enrolling: QR code and code entry;
 * codes: freshly issued recovery codes, shown once.
 */
const mode = ref<'idle' | 'enrolling' | 'codes'>('idle')
const enrollment = ref<TotpEnrollment | null>(null)
const recoveryCodes = ref<string[]>([])
const busy = ref(false)
const error = ref<unknown>(null)

const formRef = ref<FormInst | null>(null)
const form = reactive({ code: '' })
const rules: FormRules = { code: totpCodeRules() }

const enabled = computed(() => session.user?.totpEnabled === true)

/** The secret in groups of four characters, easier to type. */
const groupedSecret = computed(() => enrollment.value?.secret.match(/.{1,4}/g)?.join(' ') ?? '')

/** Runs an action, keeping a declined re-authentication silent. */
async function run(action: () => Promise<void>) {
  error.value = null
  busy.value = true
  try {
    await action()
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    busy.value = false
  }
}

function startEnrollment() {
  return run(async () => {
    enrollment.value = await beginTotp()
    form.code = ''
    mode.value = 'enrolling'
  })
}

async function finishEnrollment() {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  await run(async () => {
    try {
      recoveryCodes.value = (await confirmTotp(form.code)).recoveryCodes
    } catch (err) {
      // The pending secret expired: start over from the beginning.
      if (isApiError(err) && err.code === 'auth.totp_not_pending') cancelEnrollment()
      throw err
    }
    session.setTotpEnabled(true)
    enrollment.value = null
    mode.value = 'codes'
    message.success(t('settings.security.enabled'))
  })
}

function cancelEnrollment() {
  enrollment.value = null
  mode.value = 'idle'
}

async function regenerate() {
  const ok = await confirm({
    title: t('settings.security.confirmRegenerate.title'),
    content: t('settings.security.confirmRegenerate.content'),
    confirmText: t('settings.security.regenerate'),
  })
  if (!ok) return
  await run(async () => {
    recoveryCodes.value = (await regenerateRecoveryCodes()).recoveryCodes
    mode.value = 'codes'
  })
}

async function disable() {
  const ok = await confirm({
    title: t('settings.security.confirmDisable.title'),
    content: t('settings.security.confirmDisable.content'),
    confirmText: t('settings.security.disable'),
    danger: true,
  })
  if (!ok) return
  await run(async () => {
    await disableTotp()
    session.setTotpEnabled(false)
    message.success(t('settings.security.disabled'))
  })
}

function codesSaved() {
  recoveryCodes.value = []
  mode.value = 'idle'
}
</script>

<template>
  <NCard size="small">
    <template #header>
      <span class="title">
        {{ t('settings.security.title') }}
        <NTag :type="enabled ? 'success' : 'default'" size="small" :bordered="false">
          {{ enabled ? t('common.status.on') : t('common.status.off') }}
        </NTag>
      </span>
    </template>

    <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>

    <template v-if="mode === 'idle'">
      <NText depth="3" tag="p" class="intro">{{ t('settings.security.description') }}</NText>
      <div class="actions">
        <NButton v-if="!enabled" type="primary" :loading="busy" @click="startEnrollment">
          {{ t('settings.security.enable') }}
        </NButton>
        <template v-else>
          <NButton :disabled="busy" @click="regenerate">
            {{ t('settings.security.regenerate') }}
          </NButton>
          <NButton type="error" ghost :disabled="busy" @click="disable">
            {{ t('settings.security.disable') }}
          </NButton>
        </template>
      </div>
    </template>

    <div v-else-if="mode === 'enrolling' && enrollment" class="enroll">
      <div class="enroll__step">
        <NText tag="p" class="intro">{{ t('settings.security.enroll.scan') }}</NText>
        <TotpQrCode :uri="enrollment.uri" :label="t('settings.security.enroll.qrLabel')" />
      </div>
      <div class="enroll__step">
        <NText depth="3" tag="p" class="intro">{{ t('settings.security.enroll.manual') }}</NText>
        <code class="secret" dir="ltr">{{ groupedSecret }}</code>
      </div>
      <NForm
        ref="formRef"
        :model="form"
        :rules="rules"
        :disabled="busy"
        class="enroll__form"
        @submit.prevent="finishEnrollment"
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
          <NButton :disabled="busy" @click="cancelEnrollment">
            {{ t('common.action.cancel') }}
          </NButton>
        </div>
      </NForm>
    </div>

    <RecoveryCodes v-else-if="mode === 'codes'" :codes="recoveryCodes" @done="codesSaved" />
  </NCard>
</template>

<style scoped>
.title {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}

.block {
  margin-block-end: 16px;
}

.intro {
  margin-block: 0 12px;
}

.actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.enroll {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.enroll__form {
  max-inline-size: 320px;
}

.secret {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 15px;
  letter-spacing: 0.05em;
  overflow-wrap: anywhere;
  user-select: all;
}
</style>
