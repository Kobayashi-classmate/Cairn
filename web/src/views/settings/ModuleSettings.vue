<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { isStepUpRequired } from '@/api/client'
import { fetchModules, setModuleEnabled, type ModuleState } from '@/api/modules'
import Card from '@/components/common/Card.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import Notice from '@/components/common/Notice.vue'
import SkeletonRows from '@/components/common/SkeletonRows.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import { Switch } from '@/components/ui/switch'
import { notifyError, notifySuccess } from '@/lib/notify'
import { useNavStore } from '@/stores/nav'
import { useSessionStore } from '@/stores/session'
import SettingRow from './SettingRow.vue'
import SettingRows from './SettingRows.vue'

// Features of the panel: optional modules can be turned off, which removes
// their pages and API routes. Core modules stay on. Each switch saves at
// once (after re-authentication).
const { t, te } = useI18n()
const session = useSessionStore()
const nav = useNavStore()

const modules = ref<ModuleState[] | null>(null)
const loadError = ref<unknown>(null)
const busyId = ref<string | null>(null)

const canManage = computed(() => session.can('settings.manage'))

async function load() {
  loadError.value = null
  try {
    modules.value = await fetchModules()
  } catch (err) {
    loadError.value = err
  }
}

onMounted(load)

/** Optional modules first, then the core, each by name. */
const sorted = computed(() =>
  [...(modules.value ?? [])].sort((a, b) => Number(b.optional) - Number(a.optional) || moduleName(a.id).localeCompare(moduleName(b.id))),
)

function moduleName(id: string): string {
  const key = `modules.name.${id}`
  return te(key) ? t(key) : id
}

function moduleHint(module: ModuleState): string {
  const key = `modules.description.${module.id}`
  return te(key) ? t(key) : ''
}

async function toggle(module: ModuleState, enabled: boolean) {
  busyId.value = module.id
  try {
    const updated = await setModuleEnabled(module.id, enabled)
    modules.value = (modules.value ?? []).map((m) => (m.id === updated.id ? updated : m))
    notifySuccess(t(updated.enabled ? 'modules.enabled' : 'modules.disabled', { name: moduleName(updated.id) }))
    // Menu entries come and go with their modules.
    await nav.load()
  } catch (err) {
    if (!isStepUpRequired(err)) notifyError(err)
  } finally {
    busyId.value = null
  }
}
</script>

<template>
  <p class="m-0 -mt-2 text-muted-foreground">{{ t('modules.intro') }}</p>
  <Notice v-if="!canManage" tone="info">{{ t('settings.panel.readOnly') }}</Notice>

  <Card flush>
    <ErrorState v-if="loadError" :error="loadError" compact @retry="load" />
    <SkeletonRows v-else-if="!modules" :rows="6" />
    <SettingRows v-else>
      <SettingRow
        v-for="module in sorted"
        :key="module.id"
        :title="moduleName(module.id)"
        :description="moduleHint(module) || undefined"
        :title-id="`module-${module.id}`"
      >
        <template v-if="!module.optional" #badge>
          <StatusPill :title="t('modules.coreTooltip')">{{ t('modules.core') }}</StatusPill>
        </template>
        <span class="text-detail text-muted-foreground" aria-hidden="true">
          {{ module.enabled ? t('common.status.on') : t('common.status.off') }}
        </span>
        <Switch
          :model-value="module.enabled"
          :disabled="!module.optional || !canManage || busyId !== null"
          :aria-labelledby="`module-${module.id}`"
          @update:model-value="(value: boolean) => toggle(module, value)"
        />
      </SettingRow>
    </SettingRows>
  </Card>
</template>
