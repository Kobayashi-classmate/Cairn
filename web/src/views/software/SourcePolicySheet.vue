<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { DOWNLOAD_POLICIES, type DownloadPolicy } from '@/api/software'
import ErrorState from '@/components/common/ErrorState.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import OptionGroup from '@/components/common/OptionGroup.vue'
import type { OptionItem } from '@/components/common/types'
import { Skeleton } from '@/components/ui/skeleton'
import { errorMessage } from '@/i18n/errors'
import { notifySuccess } from '@/lib/notify'
import { useSessionStore } from '@/stores/session'
import { useSoftwareStore } from '@/stores/software'

// Which sources installations may download prebuilt runtimes and system
// packages from (the panel-wide download policy; the install sheet changes
// it too). Saved through the software store, so that kept source probes of
// the other policy are dropped. Every download is checked either way.
const open = defineModel<boolean>('open', { required: true })

const { t } = useI18n()
const session = useSessionStore()
const store = useSoftwareStore()

const canManage = computed(() => session.can('software.manage'))
const saved = ref<DownloadPolicy | null>(null)
const policy = ref<string>('')
const loadError = ref<unknown>(null)
const saving = ref(false)
const error = ref<unknown>(null)

async function load() {
  loadError.value = null
  try {
    saved.value = await store.loadPolicy()
    policy.value = saved.value
  } catch (err) {
    loadError.value = err
  }
}

watch(open, (value) => {
  if (!value) return
  error.value = null
  void load()
})

const options = computed<OptionItem[]>(() =>
  DOWNLOAD_POLICIES.map((value) => ({
    value,
    label: t(`software.policy.${value}.short`),
    hint: t(`software.policy.${value}.description`),
  })),
)

async function submit() {
  if (!canManage.value || !policy.value) return
  if (policy.value === saved.value) {
    open.value = false
    return
  }
  saving.value = true
  error.value = null
  try {
    saved.value = await store.savePolicy(policy.value as DownloadPolicy)
    notifySuccess(t('software.policy.saved'))
    open.value = false
  } catch (err) {
    error.value = err
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <FormSheet
    v-model:open="open"
    :title="t('software.policy.title')"
    :description="t('software.policy.intro')"
    :submit-label="t('common.action.save')"
    :submitting="saving"
    :submit-disabled="!canManage || saved === null"
    @submit="submit"
  >
    <Notice v-if="!canManage" tone="info">{{ t('software.policy.readOnly') }}</Notice>
    <ErrorState v-if="loadError" :error="loadError" compact @retry="load" />
    <div v-else-if="saved === null" class="flex flex-col gap-2" aria-hidden="true">
      <Skeleton class="h-16 w-full rounded-md" />
      <Skeleton class="h-16 w-full rounded-md" />
    </div>
    <OptionGroup v-else v-model="policy" :options="options" :disabled="!canManage" :label="t('software.policy.title')" />
    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
