<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { NAlert, NEmpty, NH2, NSpin, NTabPane, NTabs, NText } from 'naive-ui'
import { fetchDatabases, type DbStatus } from '@/api/databases'
import { engineLabel } from '@/databases/labels'
import { errorMessage } from '@/i18n/errors'
import { useSessionStore } from '@/stores/session'
import EngineHeader from './databases/EngineHeader.vue'
import RedisPanel from './databases/RedisPanel.vue'
import SqlPanel from './databases/SqlPanel.vue'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const session = useSessionStore()

const status = ref<DbStatus | null>(null)
const loadError = ref<unknown>(null)
/** Bumped on every reload so panels that keep their own lists (dumps) refresh too. */
const version = ref(0)
const tab = ref<string | undefined>(typeof route.query.engine === 'string' ? route.query.engine : undefined)

const canManage = computed(() => session.can('databases.manage'))
/** Sub-tab asked for in the address (?tab=dumps from the backups page). */
const subTab = route.query.tab === 'dumps' || route.query.tab === 'users' ? route.query.tab : undefined

async function load() {
  try {
    status.value = await fetchDatabases()
    loadError.value = null
    version.value++
    const engines = status.value.engines
    if (!tab.value || !engines.some((e) => e.id === tab.value)) tab.value = engines[0]?.id
  } catch (err) {
    loadError.value = err
  }
}

onMounted(load)

// The engine tab is kept in the address.
watch(tab, (value) => {
  if (value) void router.replace({ query: { ...route.query, engine: value } })
})
</script>

<template>
  <div class="databases">
    <NH2 class="databases__title">{{ t('databases.title') }}</NH2>

    <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>

    <NSpin :show="!status && !loadError">
      <template v-if="status">
        <NAlert v-if="!status.supported" type="warning" class="block">
          {{ status.reason === 'platform' ? t('databases.unsupported.platform') : t('errors.db.unsupported') }}
        </NAlert>
        <template v-else>
          <NAlert v-if="!status.statusAvailable" type="info" class="block">{{ t('databases.statusUnavailable') }}</NAlert>
          <NEmpty v-if="status.engines.length === 0" :description="t('databases.empty.title')" class="empty">
            <template #extra>
              <NText depth="3" tag="p" class="empty__text">{{ t('databases.empty.text') }}</NText>
              <RouterLink :to="{ name: 'software' }">{{ t('databases.empty.install') }}</RouterLink>
            </template>
          </NEmpty>
          <NTabs v-else v-model:value="tab" type="line">
            <NTabPane v-for="engine in status.engines" :key="engine.id" :name="engine.id" :tab="engineLabel(engine)">
              <EngineHeader :engine="engine" :can-manage="canManage" @changed="load" />
              <RedisPanel
                v-if="engine.kind === 'redis'"
                :engine="engine"
                :can-manage="canManage"
                :version="version"
                @changed="load"
              />
              <SqlPanel
                v-else
                :engine="engine"
                :can-manage="canManage"
                :version="version"
                :initial-tab="subTab"
                @changed="load"
              />
            </NTabPane>
          </NTabs>
        </template>
      </template>
    </NSpin>
  </div>
</template>

<style scoped>
.databases {
  max-inline-size: 1200px;
}

.databases__title {
  margin-block: 0 12px;
}

.block {
  margin-block-end: 16px;
}

.empty {
  padding-block: 40px;
}

.empty__text {
  max-inline-size: 520px;
  margin: 0 auto 12px;
  text-align: center;
}
</style>
