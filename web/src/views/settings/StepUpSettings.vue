<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { isStepUpRequired } from '@/api/client'
import { setStepUp } from '@/api/settings'
import Card from '@/components/common/Card.vue'
import Notice from '@/components/common/Notice.vue'
import { Switch } from '@/components/ui/switch'
import { notifyError, notifySuccess } from '@/lib/notify'
import { useSessionStore } from '@/stores/session'
import SettingRow from './SettingRow.vue'
import SettingRows from './SettingRows.vue'

// Whether sensitive operations ask for the password again (the panel's
// step-up switch, kept in the session store). It saves at once: turning it
// off is a sensitive operation itself, turning it back on is not. API
// tokens stay refused on those operations either way.
const { t } = useI18n()
const session = useSessionStore()

const canManage = computed(() => session.can('settings.manage'))
/** The value being saved, shown until the server answers. */
const saving = ref<boolean | null>(null)
const enabled = computed(() => saving.value ?? session.stepUpRequired)

async function toggle(value: boolean) {
  if (!canManage.value || saving.value !== null || value === session.stepUpRequired) return
  saving.value = value
  try {
    const answer = await setStepUp(value)
    notifySuccess(t(answer.enabled ? 'settings.stepUp.enabled' : 'settings.stepUp.disabled'))
  } catch (err) {
    // The switch shows the stored value again; a declined
    // re-authentication stays quiet.
    if (!isStepUpRequired(err)) notifyError(err)
  } finally {
    saving.value = null
  }
}
</script>

<template>
  <Card flush>
    <SettingRows>
      <SettingRow :title="t('settings.stepUp.title')" :description="t('settings.stepUp.description')" title-id="panel-step-up">
        <span class="text-detail text-muted-foreground" aria-hidden="true">
          {{ enabled ? t('common.status.on') : t('common.status.off') }}
        </span>
        <Switch
          :model-value="enabled"
          :disabled="!canManage || saving !== null"
          aria-labelledby="panel-step-up"
          @update:model-value="toggle"
        />
      </SettingRow>
    </SettingRows>
    <Notice v-if="!enabled" tone="warning" class="mx-4 mb-4 md:mx-[18px]">{{ t('settings.stepUp.offWarning') }}</Notice>
  </Card>
</template>
