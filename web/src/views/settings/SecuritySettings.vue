<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { KeyRound, ShieldCheck, ShieldOff } from '@lucide/vue'
import { disableTotp, regenerateRecoveryCodes } from '@/api/account'
import Card from '@/components/common/Card.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import RecoveryCodes from '@/components/RecoveryCodes.vue'
import TotpEnrollment from '@/components/TotpEnrollment.vue'
import { Button } from '@/components/ui/button'
import { confirmAction } from '@/composables/useConfirmDialog'
import { notifySuccess } from '@/lib/notify'
import { useSessionStore } from '@/stores/session'
import SettingRow from './SettingRow.vue'
import SettingRows from './SettingRows.vue'

// Two-step verification for your own account: set it up (QR code, a code
// from the app, recovery codes shown once), generate new recovery codes or
// turn it off. Each change asks to re-authenticate.
const { t } = useI18n()
const session = useSessionStore()

/** idle: state and actions; enrolling: the set-up flow; codes: new recovery codes, shown once. */
const mode = ref<'idle' | 'enrolling' | 'codes'>('idle')
const recoveryCodes = ref<string[]>([])

const enabled = computed(() => session.user?.totpEnabled === true)

function askRegenerate() {
  void confirmAction({
    title: t('settings.security.confirmRegenerate.title'),
    description: t('settings.security.confirmRegenerate.content'),
    confirmLabel: t('settings.security.regenerate'),
    run: async () => {
      recoveryCodes.value = (await regenerateRecoveryCodes()).recoveryCodes
      mode.value = 'codes'
    },
  })
}

function askDisable() {
  void confirmAction({
    title: t('settings.security.confirmDisable.title'),
    description: t('settings.security.confirmDisable.content'),
    confirmLabel: t('settings.security.disable'),
    destructive: true,
    run: async () => {
      await disableTotp()
      session.setTotpEnabled(false)
      notifySuccess(t('settings.security.disabled'))
    },
  })
}

function backToIdle() {
  recoveryCodes.value = []
  mode.value = 'idle'
}
</script>

<template>
  <Card flush>
    <SettingRows>
      <SettingRow :title="t('settings.security.title')" :description="t('settings.security.description')">
        <template #badge>
          <StatusPill :tone="enabled ? 'success' : 'warning'" dot>
            {{ enabled ? t('settings.security.stateOn') : t('settings.security.stateOff') }}
          </StatusPill>
        </template>
        <template v-if="mode === 'idle'">
          <Button v-if="!enabled" class="max-md:h-11" @click="mode = 'enrolling'">
            <ShieldCheck aria-hidden="true" />
            {{ t('settings.security.enable') }}
          </Button>
          <Button v-else variant="outline" class="text-destructive hover:text-destructive max-md:h-11" @click="askDisable">
            <ShieldOff aria-hidden="true" />
            {{ t('settings.security.disable') }}
          </Button>
        </template>
      </SettingRow>
      <SettingRow
        v-if="enabled && mode === 'idle'"
        :title="t('settings.security.recoveryTitle')"
        :description="t('settings.security.recoveryHint')"
      >
        <Button variant="outline" class="max-md:h-11" @click="askRegenerate">
          <KeyRound aria-hidden="true" />
          {{ t('settings.security.regenerate') }}
        </Button>
      </SettingRow>
      <div v-if="mode === 'enrolling'" class="px-4 py-4 md:px-[18px]">
        <TotpEnrollment @done="backToIdle" @cancel="backToIdle" />
      </div>
      <div v-else-if="mode === 'codes'" class="px-4 py-4 md:px-[18px]">
        <RecoveryCodes :codes="recoveryCodes" @done="backToIdle" />
      </div>
    </SettingRows>
  </Card>
</template>
