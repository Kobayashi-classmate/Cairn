<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NCard, NTag, NText, useMessage } from 'naive-ui'
import { disableTotp, regenerateRecoveryCodes } from '@/api/account'
import { isStepUpRequired } from '@/api/client'
import RecoveryCodes from '@/components/RecoveryCodes.vue'
import TotpEnrollment from '@/components/TotpEnrollment.vue'
import { useConfirm } from '@/composables/useConfirm'
import { errorMessage } from '@/i18n/errors'
import { useSessionStore } from '@/stores/session'

const { t } = useI18n()
const message = useMessage()
const confirm = useConfirm()
const session = useSessionStore()

/**
 * idle: status and actions; enrolling: the enrollment flow;
 * codes: newly generated recovery codes, shown once.
 */
const mode = ref<'idle' | 'enrolling' | 'codes'>('idle')
const recoveryCodes = ref<string[]>([])
const busy = ref(false)
const error = ref<unknown>(null)

const enabled = computed(() => session.user?.totpEnabled === true)

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
  error.value = null
  mode.value = 'enrolling'
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

function backToIdle() {
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

    <TotpEnrollment v-else-if="mode === 'enrolling'" @done="backToIdle" @cancel="backToIdle" />

    <RecoveryCodes v-else-if="mode === 'codes'" :codes="recoveryCodes" @done="backToIdle" />
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
</style>
