<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { isStepUpRequired } from '@/api/client'
import { fetchSettings, updateDefaultLocale } from '@/api/settings'
import Card from '@/components/common/Card.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import Notice from '@/components/common/Notice.vue'
import SelectField from '@/components/common/SelectField.vue'
import type { SelectOption } from '@/components/common/types'
import { Skeleton } from '@/components/ui/skeleton'
import { certsAvailable } from '@/certs/useCertChoices'
import { localeNativeName } from '@/i18n'
import { notifyError, notifySuccess } from '@/lib/notify'
import { useNavStore } from '@/stores/nav'
import { useSessionStore } from '@/stores/session'
import DownloadPolicySettings from './DownloadPolicySettings.vue'
import PanelCertSettings from './PanelCertSettings.vue'
import SettingRow from './SettingRow.vue'
import SettingRows from './SettingRows.vue'

// Panel-wide settings: the default language, the certificate the panel
// serves and where runtimes are downloaded from. Quick changes save at once.
const { t } = useI18n()
const session = useSessionStore()
const nav = useNavStore()

const canManage = computed(() => session.can('settings.manage'))
// Only when the certificates module is on and the user may read certificates.
const showPanelCert = computed(() => certsAvailable())
// The menu lists the software page only when its module is on and the user
// may read it, which is exactly when its download policy is worth showing.
const showDownloadPolicy = computed(
  () => session.can('software.read') && nav.entries.some((entry) => entry.route === 'software'),
)

const defaultLocale = ref<string | null>(null)
const loadError = ref<unknown>(null)
const saving = ref(false)

async function load() {
  loadError.value = null
  try {
    defaultLocale.value = (await fetchSettings()).defaultLocale
  } catch (err) {
    loadError.value = err
  }
}

onMounted(load)

const localeOptions = computed<SelectOption[]>(() =>
  session.supportedLocales.map((code) => ({ value: code, label: localeNativeName(code) })),
)

async function changeLocale(value: string) {
  const previous = defaultLocale.value
  if (!canManage.value || value === previous) return
  defaultLocale.value = value
  saving.value = true
  try {
    await updateDefaultLocale(value)
    if (session.meta) session.meta.defaultLocale = value
    notifySuccess(t('settings.panel.saved'))
  } catch (err) {
    defaultLocale.value = previous
    if (!isStepUpRequired(err)) notifyError(err)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <Notice v-if="!canManage" tone="info">{{ t('settings.panel.readOnly') }}</Notice>

  <Card flush>
    <ErrorState v-if="loadError" :error="loadError" compact @retry="load" />
    <SettingRows v-else>
      <SettingRow
        :title="t('settings.panel.defaultLocale')"
        :description="t('settings.panel.defaultLocaleHint')"
        label-for="panel-default-locale"
      >
        <Skeleton v-if="defaultLocale === null" class="h-9 w-full @lg:w-56" />
        <SelectField
          v-else
          id="panel-default-locale"
          :model-value="defaultLocale"
          :options="localeOptions"
          :disabled="!canManage || saving"
          class="w-full @lg:w-56"
          @update:model-value="changeLocale"
        />
      </SettingRow>
    </SettingRows>
  </Card>

  <PanelCertSettings v-if="showPanelCert" />
  <DownloadPolicySettings v-if="showDownloadPolicy" />
</template>
