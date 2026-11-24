<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Blocks, Download, FileCog, Play, RotateCw, ScrollText, Server, Square, Tag, Trash2 } from '@lucide/vue'
import { isAvailable, isEndOfLifeInstalled, isManageable } from '@/api/software'
import EmptyState from '@/components/common/EmptyState.vue'
import Notice from '@/components/common/Notice.vue'
import SegmentedControl from '@/components/common/SegmentedControl.vue'
import SettingsDrawer from '@/components/common/SettingsDrawer.vue'
import type { DrawerItem } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { useDrawerRoute } from '@/composables/useDrawerRoute'
import { useNavRoutes } from '@/composables/useNavRoutes'
import {
  LINE_STATE_TONE,
  categoryIcon,
  endOfLifeNotice,
  installedOffers,
  installedVersion,
  lineKey,
  lineState,
  lineStateText,
  methodText,
  parseLineKey,
  reasonText,
} from '@/software/display'
import { useSessionStore } from '@/stores/session'
import { useSoftwareStore } from '@/stores/software'
import { askUninstall, controlLine } from './actions'
import ConfigPane from './drawer/ConfigPane.vue'
import ExtensionsPane from './drawer/ExtensionsPane.vue'
import LogsPane from './drawer/LogsPane.vue'
import ServicePane from './drawer/ServicePane.vue'
import VersionPane from './drawer/VersionPane.vue'

// A software line's settings drawer (interface spec, section 6), opened
// from the software page and from a database server's card:
// ?open=software:<id>@<line>&section=<sub-item>. The sub-items are the ones
// the line has: its services, version, PHP extensions, configuration files
// and service logs; Uninstall is apart at the bottom.
type Section = 'service' | 'version' | 'extensions' | 'configs' | 'logs'

const { t } = useI18n()
const store = useSoftwareStore()
const session = useSessionStore()
const navRoutes = useNavRoutes()
const drawer = useDrawerRoute('software')

const key = computed(() => (drawer.id.value ? parseLineKey(drawer.id.value) : null))

watch(
  () => drawer.id.value,
  (id) => {
    if (id) void store.ensure()
  },
  { immediate: true },
)

const entry = computed(() => (key.value ? store.entry(key.value.software) : null))
/** The line asked for, or the first installed one when the address names none. */
const offer = computed(() => {
  const current = entry.value
  if (!current || !key.value) return null
  if (!key.value.line) return installedOffers(current)[0] ?? null
  return current.offers.find((o) => o.line === key.value?.line) ?? null
})
const installed = computed(() => offer.value?.installed === true)
const lines = computed(() => (entry.value ? installedOffers(entry.value) : []))

const canManage = computed(() => session.can('software.manage'))
// Configuration files are as good as root (nginx loads modules and writes
// logs where it is told): administrators only.
const canConfig = computed(() => session.can('software.config'))
const canReadLogs = computed(() => session.can('logs.read') && navRoutes.value.has('logs'))
const hasUnits = computed(() => (offer.value?.units?.length ?? 0) > 0)
const busy = computed(() => (entry.value ? store.runningFor(entry.value.id) !== null : false))

const items = computed<DrawerItem<Section>[]>(() => {
  const list: DrawerItem<Section>[] = []
  const o = offer.value
  if (installed.value && hasUnits.value) list.push({ value: 'service', label: t('software.drawer.service'), icon: Server })
  list.push({ value: 'version', label: t('software.drawer.version'), icon: Tag })
  if (installed.value && o?.extensions) list.push({ value: 'extensions', label: t('software.drawer.extensions'), icon: Blocks })
  if (installed.value && canConfig.value && (o?.configs?.length ?? 0) > 0) {
    list.push({ value: 'configs', label: t('software.drawer.configs'), icon: FileCog })
  }
  if (installed.value && hasUnits.value && canReadLogs.value) list.push({ value: 'logs', label: t('software.drawer.logs'), icon: ScrollText })
  return list
})

const dangerItems = computed(() =>
  canManage.value && installed.value && offer.value && isManageable(offer.value)
    ? [{ value: 'uninstall', label: t('software.drawer.uninstall'), icon: Trash2 }]
    : [],
)

const section = computed<Section>(() => {
  const wanted = drawer.section.value
  return items.value.find((item) => item.value === wanted)?.value ?? items.value[0]?.value ?? 'version'
})

const state = computed(() => (offer.value && installed.value ? lineState(offer.value) : null))
const status = computed(() =>
  state.value
    ? { tone: LINE_STATE_TONE[state.value], label: lineStateText(state.value) }
    : offer.value
      ? { tone: 'neutral' as const, label: t('software.notInstalled') }
      : null,
)

const title = computed(() => {
  if (entry.value && offer.value) return `${entry.value.name} ${offer.value.line}`
  return entry.value?.name ?? key.value?.software ?? ''
})

const meta = computed(() => {
  const o = offer.value
  if (!o) return ''
  const parts = [installedVersion(o), methodText(o, store.repos)].filter(Boolean)
  return parts.join(' · ')
})

const lineOptions = computed(() => lines.value.map((o) => ({ value: o.line, label: o.line })))

function switchLine(line: string) {
  if (!entry.value || line === offer.value?.line) return
  drawer.open(lineKey(entry.value.id, line), section.value)
}

const loading = computed(() => drawer.id.value !== null && !store.catalog && !store.loadError)
const error = computed(() => (store.catalog ? null : store.loadError))

async function onDanger() {
  if (!entry.value || !offer.value) return
  if (await askUninstall(entry.value, offer.value)) drawer.close()
}

function control(action: 'start' | 'stop' | 'restart') {
  if (entry.value && offer.value) void controlLine(entry.value, offer.value, action)
}

function install() {
  if (entry.value) store.openInstall(entry.value.id, offer.value?.line)
}
</script>

<template>
  <SettingsDrawer
    :open="drawer.id.value !== null"
    :title="title"
    :icon="entry ? categoryIcon(entry.category) : Server"
    :status="status"
    :meta="meta"
    :items="items"
    :danger-items="dangerItems"
    :section="section"
    :loading="loading"
    :error="error"
    @close="drawer.close()"
    @update:section="drawer.setSection"
    @danger="onDanger"
    @retry="store.load()"
  >
    <template v-if="entry && offer" #actions>
      <SegmentedControl
        v-if="lines.length > 1"
        :model-value="offer.line"
        :options="lineOptions"
        :label="t('software.drawer.lineSwitch', { name: entry.name })"
        class="font-mono"
        @update:model-value="switchLine"
      />
      <template v-if="canManage && installed && hasUnits">
        <template v-if="state === 'running' || state === 'partial'">
          <Button variant="outline" size="sm" class="max-md:h-11" :disabled="busy" @click="control('restart')">
            <RotateCw aria-hidden="true" />
            {{ t('software.service.restart') }}
          </Button>
          <Button variant="outline" size="sm" class="max-md:h-11" :disabled="busy" @click="control('stop')">
            <Square aria-hidden="true" />
            {{ t('software.service.stop') }}
          </Button>
        </template>
        <Button v-else variant="outline" size="sm" class="max-md:h-11" :disabled="busy" @click="control('start')">
          <Play aria-hidden="true" />
          {{ t('software.service.start') }}
        </Button>
      </template>
    </template>

    <template v-if="entry && offer">
      <!-- Not installed and not installable: nothing to manage, nothing to offer. -->
      <EmptyState
        v-if="!installed && section === 'version' && !isAvailable(offer)"
        :title="t('software.drawer.notAvailable', { name: title })"
        :text="offer.method ? reasonText(offer) : t('software.reason.unknown')"
      />
      <template v-else>
        <Notice v-if="isEndOfLifeInstalled(offer)" tone="warning">{{ endOfLifeNotice(entry.name, offer) }}</Notice>
        <EmptyState
          v-if="!installed"
          :icon="Download"
          compact
          class="rounded-lg border bg-card"
          :title="t('software.drawer.notInstalled', { name: title })"
          :text="t('software.drawer.notInstalledText')"
        >
          <template v-if="canManage" #actions>
            <Button :disabled="busy" @click="install">
              <Download aria-hidden="true" />
              {{ t('software.action.install') }}
            </Button>
          </template>
        </EmptyState>
        <ServicePane v-if="section === 'service'" :entry="entry" :offer="offer" :can-manage="canManage" />
        <VersionPane v-else-if="section === 'version'" :entry="entry" :offer="offer" :can-manage="canManage" />
        <ExtensionsPane v-else-if="section === 'extensions'" :key="lineKey(entry.id, offer.line)" :entry="entry" :offer="offer" :can-manage="canManage" />
        <ConfigPane v-else-if="section === 'configs'" :key="lineKey(entry.id, offer.line)" :entry="entry" :offer="offer" />
        <LogsPane v-else-if="section === 'logs'" :key="lineKey(entry.id, offer.line)" :offer="offer" />
      </template>
    </template>
    <EmptyState
      v-else-if="store.catalog"
      :title="t('software.drawer.unknown', { name: drawer.id.value ?? '' })"
      :text="t('software.drawer.unknownText')"
    />
  </SettingsDrawer>
</template>
