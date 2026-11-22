<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { fetchSoftwareSettings, type DownloadPolicy } from '@/api/software'
import Card from '@/components/common/Card.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useSessionStore } from '@/stores/session'
import SourcePolicySheet from '@/views/software/SourcePolicySheet.vue'
import SettingRow from './SettingRow.vue'
import SettingRows from './SettingRows.vue'

// Where prebuilt runtimes are downloaded from (the panel-wide download
// policy); changed in the same sheet as on the software page.
const { t } = useI18n()
const session = useSessionStore()

const canManage = computed(() => session.can('software.manage'))
const policy = ref<DownloadPolicy | null>(null)
const loadError = ref<unknown>(null)
const showSheet = ref(false)

async function load() {
  loadError.value = null
  try {
    policy.value = (await fetchSoftwareSettings()).downloadPolicy
  } catch (err) {
    loadError.value = err
  }
}

onMounted(load)
// The sheet saves on its own; read the policy again once it closes.
watch(showSheet, (open) => {
  if (!open) void load()
})
</script>

<template>
  <Card flush>
    <ErrorState v-if="loadError" :error="loadError" compact @retry="load" />
    <SettingRows v-else>
      <SettingRow :title="t('software.policy.title')">
        <template #description>
          <Skeleton v-if="policy === null" class="mt-1 h-4 w-64 max-w-full" />
          <template v-else>
            <span class="font-medium text-foreground">{{ t(`software.policy.${policy}.short`) }}</span>
            · {{ t(`software.policy.${policy}.description`) }}
          </template>
        </template>
        <Button variant="outline" class="max-md:h-11" :disabled="policy === null" @click="showSheet = true">
          {{ canManage ? t('settings.downloadPolicy.change') : t('settings.downloadPolicy.view') }}
        </Button>
      </SettingRow>
    </SettingRows>
  </Card>

  <SourcePolicySheet v-model:open="showSheet" />
</template>
