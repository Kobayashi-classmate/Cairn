<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import Card from '@/components/common/Card.vue'
import KeyValueList from '@/components/common/KeyValueList.vue'
import KeyValueRow from '@/components/common/KeyValueRow.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import { Button } from '@/components/ui/button'
import { roleLabel } from '@/auth/permissions'
import { useSessionStore } from '@/stores/session'
import PasswordSheet from './PasswordSheet.vue'
import SettingRow from './SettingRow.vue'
import SettingRows from './SettingRows.vue'

// Your account: who you are, your password and a pointer to two-step
// verification. Colour scheme and language live under Appearance.
const { t } = useI18n()
const session = useSessionStore()
const user = computed(() => session.user)
const showPassword = ref(false)
</script>

<template>
  <template v-if="user">
    <Card :title="t('settings.account.profile')" :level="3" flush>
      <KeyValueList>
        <KeyValueRow :label="t('common.field.username')">
          <span class="font-medium" dir="auto">{{ user.username }}</span>
        </KeyValueRow>
        <KeyValueRow :label="t('common.field.role')">{{ roleLabel(user.role) }}</KeyValueRow>
        <KeyValueRow :label="t('settings.account.totp')">
          <StatusPill :tone="user.totpEnabled ? 'success' : 'warning'">
            {{ user.totpEnabled ? t('settings.security.stateOn') : t('settings.security.stateOff') }}
          </StatusPill>
          <RouterLink :to="{ name: 'settings-security' }" class="text-detail text-link underline-offset-2 hover:underline">
            {{ t('settings.account.manageTotp') }}
          </RouterLink>
        </KeyValueRow>
      </KeyValueList>
    </Card>

    <Card flush>
      <SettingRows>
        <SettingRow :title="t('settings.account.password')" :description="t('settings.account.passwordHint')">
          <Button variant="outline" class="max-md:h-11" @click="showPassword = true">
            {{ t('settings.account.passwordSubmit') }}
          </Button>
        </SettingRow>
      </SettingRows>
    </Card>

    <PasswordSheet v-model:open="showPassword" />
  </template>
</template>
