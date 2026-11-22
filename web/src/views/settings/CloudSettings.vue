<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { LoaderCircle } from '@lucide/vue'
import { isStepUpRequired } from '@/api/client'
import { CLOUD_CHANNELS, fetchCloudSettings, saveCloudSettings, type CloudChannel, type CloudSettings } from '@/api/settings'
import Card from '@/components/common/Card.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import Notice from '@/components/common/Notice.vue'
import SelectField from '@/components/common/SelectField.vue'
import type { SelectOption } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { isHttpsUrl } from '@/forms/rules'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { notifySuccess } from '@/lib/notify'
import { useSessionStore } from '@/stores/session'
import SettingRow from './SettingRow.vue'
import SettingRows from './SettingRows.vue'

// Where the panel downloads signed public data (official messages, release
// information): the official service, a mirror, or nothing at all.
// Saving asks to re-authenticate.
const { t } = useI18n()
const session = useSessionStore()

const canManage = computed(() => session.can('settings.manage'))
const saved = ref<CloudSettings | null>(null)
const form = reactive({ enabled: true, baseUrl: '', channel: 'stable' as string })
const urlError = ref<string | null>(null)
const loadError = ref<unknown>(null)
const saving = ref(false)
const error = ref<unknown>(null)

async function load() {
  loadError.value = null
  try {
    saved.value = await fetchCloudSettings()
    Object.assign(form, saved.value)
  } catch (err) {
    loadError.value = err
  }
}

onMounted(load)

const channelOptions = computed<SelectOption[]>(() =>
  CLOUD_CHANNELS.map((value) => ({ value, label: t(`updates.channel.${value}`) })),
)

const changed = computed(
  () =>
    !!saved.value &&
    (form.enabled !== saved.value.enabled || form.baseUrl.trim() !== saved.value.baseUrl || form.channel !== saved.value.channel),
)

function validate(): boolean {
  const url = form.baseUrl.trim()
  urlError.value = !url ? validationMessage('baseUrl', 'required') : !isHttpsUrl(url) ? validationMessage('baseUrl', 'https') : null
  return urlError.value === null
}

async function save() {
  if (!canManage.value || !validate()) return
  error.value = null
  saving.value = true
  try {
    const next: CloudSettings = { enabled: form.enabled, baseUrl: form.baseUrl.trim(), channel: form.channel as CloudChannel }
    await saveCloudSettings(next)
    saved.value = next
    form.baseUrl = next.baseUrl
    notifySuccess(t('settings.panel.saved'))
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    saving.value = false
  }
}

function reset() {
  if (saved.value) Object.assign(form, saved.value)
  urlError.value = null
  error.value = null
}
</script>

<template>
  <Notice v-if="!canManage" tone="info">{{ t('settings.panel.readOnly') }}</Notice>

  <Card flush>
    <ErrorState v-if="loadError" :error="loadError" compact @retry="load" />
    <div v-else-if="!saved" class="flex flex-col gap-3 px-4 py-4 md:px-[18px]" role="status">
      <span class="sr-only">{{ t('common.state.loading') }}</span>
      <Skeleton class="h-5 w-64 max-w-full" />
      <Skeleton class="h-9 w-full" />
      <Skeleton class="h-9 w-48" />
    </div>
    <form v-else novalidate @submit.prevent="save">
      <SettingRows>
        <SettingRow
          :title="t('updates.cloud.enabled')"
          :description="`${t('updates.cloud.helpDownloadOnly')} ${t('updates.cloud.helpDisable')}`"
          title-id="cloud-enabled-title"
        >
          <Switch
            v-model="form.enabled"
            :disabled="!canManage || saving"
            aria-labelledby="cloud-enabled-title"
          />
        </SettingRow>
        <SettingRow :title="t('common.field.baseUrl')" :description="t('updates.cloud.helpMirror')" label-for="cloud-url">
          <div class="flex w-full flex-col gap-1 @lg:w-80">
            <Input
              id="cloud-url"
              v-model="form.baseUrl"
              type="url"
              autocomplete="off"
              autocapitalize="none"
              spellcheck="false"
              dir="ltr"
              class="font-mono"
              :disabled="!canManage || saving"
              :aria-invalid="urlError ? true : undefined"
              :aria-describedby="urlError ? 'cloud-url-error' : undefined"
            />
            <p v-if="urlError" id="cloud-url-error" class="m-0 text-xs font-medium text-destructive">{{ urlError }}</p>
          </div>
        </SettingRow>
        <SettingRow :title="t('common.field.channel')" :description="t('settings.cloud.channelHint')" label-for="cloud-channel">
          <SelectField
            id="cloud-channel"
            v-model="form.channel"
            :options="channelOptions"
            :disabled="!canManage || saving"
            class="w-full @lg:w-48"
          />
        </SettingRow>
      </SettingRows>
      <div v-if="canManage" class="flex flex-wrap items-center justify-end gap-2 border-t px-4 py-3 md:px-[18px]">
        <Notice v-if="error" tone="destructive" class="w-full">{{ errorMessage(error) }}</Notice>
        <Button type="button" variant="outline" class="max-md:h-11" :disabled="!changed || saving" @click="reset">
          {{ t('settings.cloud.discard') }}
        </Button>
        <Button type="submit" class="max-md:h-11" :disabled="!changed || saving">
          <LoaderCircle v-if="saving" class="animate-spin" aria-hidden="true" />
          {{ t('common.action.save') }}
        </Button>
      </div>
    </form>
  </Card>
</template>
