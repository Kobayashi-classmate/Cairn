<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NCard, NSpin, NSwitch, NText, NTooltip, useMessage } from 'naive-ui'
import { fetchModules, setModuleEnabled, type ModuleState } from '@/api/modules'
import { useErrorToast } from '@/composables/useErrorToast'
import { errorMessage } from '@/i18n/errors'
import { useNavStore } from '@/stores/nav'
import { useSessionStore } from '@/stores/session'

const { t, te } = useI18n()
const message = useMessage()
const showError = useErrorToast()
const session = useSessionStore()
const nav = useNavStore()

const modules = ref<ModuleState[]>([])
const loading = ref(true)
const loadError = ref<unknown>(null)
const busyId = ref<string | null>(null)

const canManage = computed(() => session.can('settings.manage'))

onMounted(async () => {
  try {
    modules.value = await fetchModules()
  } catch (err) {
    loadError.value = err
  } finally {
    loading.value = false
  }
})

function moduleName(id: string): string {
  const key = `modules.name.${id}`
  return te(key) ? t(key) : id
}

async function toggle(module: ModuleState, enabled: boolean) {
  busyId.value = module.id
  try {
    const updated = await setModuleEnabled(module.id, enabled)
    modules.value = modules.value.map((m) => (m.id === updated.id ? updated : m))
    message.success(
      t(updated.enabled ? 'modules.enabled' : 'modules.disabled', { name: moduleName(updated.id) }),
    )
    // Menu entries come and go with their modules.
    await nav.load()
  } catch (err) {
    showError(err)
  } finally {
    busyId.value = null
  }
}
</script>

<template>
  <NCard :title="t('modules.title')" size="small">
    <NText depth="3" tag="p" class="intro">{{ t('modules.intro') }}</NText>
    <NAlert v-if="!canManage" type="info" class="block">{{ t('settings.panel.readOnly') }}</NAlert>
    <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>
    <NSpin :show="loading">
      <ul class="modules">
        <li v-for="module in modules" :key="module.id" class="module">
          <div class="module__text">
            <span class="module__name">{{ moduleName(module.id) }}</span>
            <NText v-if="!module.optional" depth="3" class="module__note">{{ t('modules.core') }}</NText>
          </div>
          <NTooltip :disabled="module.optional">
            <template #trigger>
              <!-- A wrapper keeps the tooltip working on a disabled switch. -->
              <span>
                <NSwitch
                  :value="module.enabled"
                  :disabled="!module.optional || !canManage || (busyId !== null && busyId !== module.id)"
                  :loading="busyId === module.id"
                  :aria-label="moduleName(module.id)"
                  @update:value="(value: boolean) => toggle(module, value)"
                />
              </span>
            </template>
            {{ t('modules.coreTooltip') }}
          </NTooltip>
        </li>
      </ul>
    </NSpin>
  </NCard>
</template>

<style scoped>
.intro {
  margin-block: 0 12px;
}

.block {
  margin-block-end: 12px;
}

.modules {
  margin: 0;
  padding: 0;
  list-style: none;
}

.module {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding-block: 10px;
  border-block-end: 1px solid rgb(128 128 128 / 15%);
}

.module:last-child {
  border-block-end: 0;
}

.module__text {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.module__name {
  font-weight: 500;
}

.module__note {
  font-size: 13px;
}
</style>
