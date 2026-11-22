<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, type RouteLocationRaw } from 'vue-router'
import { Database, Globe, Lock, Play, RotateCw, Settings2, Square, Zap } from '@lucide/vue'
import type { DbEngine } from '@/api/databases'
import RowActions from '@/components/common/RowActions.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import type { RowAction } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { useDrawerRoute } from '@/composables/useDrawerRoute'
import { engineLabel } from '@/databases/labels'
import { errorText } from '@/i18n/errors'
import { formatBytes, formatNumber } from '@/i18n/format'
import { cn } from '@/lib/utils'
import { lineKey } from '@/software/display'
import { useSessionStore } from '@/stores/session'
import { engineStateText, engineTone, serviceAction, toggleRemote } from './actions'

// One card per database server (interface spec, section 6): its state, port,
// who may connect and how much it holds. The selected card is the page's
// tab (?tab=<engine id>); its actions start, stop or restart the server,
// switch remote access and open the server's software settings.
const props = defineProps<{
  engines: readonly DbEngine[]
  selected: string | undefined
  location: (id: string) => RouteLocationRaw
  canManage: boolean
}>()

const { t } = useI18n()
const session = useSessionStore()
const softwareDrawer = useDrawerRoute('software')

const canOpenSoftware = computed(() => session.can('software.read'))

function meta(engine: DbEngine): string {
  const parts: string[] = []
  if (engine.port) parts.push(t('databases.port', { port: engine.port }))
  parts.push(engine.remote ? t('databases.card.remote') : t('databases.card.local'))
  if (engine.kind === 'redis' && engine.redis) {
    parts.push(engine.redis.passwordSet ? t('databases.redis.passwordOn') : t('databases.redis.passwordOff'))
    parts.push(t('databases.card.keys', { count: formatNumber(engine.redis.keys) }, engine.redis.keys))
    parts.push(formatBytes(engine.redis.usedMemory))
  } else if (engine.kind !== 'redis') {
    const count = engine.databases.filter((db) => !db.missing).length
    parts.push(t('databases.card.databases', { count }, count))
  }
  return parts.join(' · ')
}

function failure(engine: DbEngine): string {
  return engine.error ? (errorText(engine.error.code, engine.error.params ?? {}) ?? engine.error.code) : ''
}

function actions(engine: DbEngine): RowAction[] {
  if (!props.canManage) return []
  const list: RowAction[] = []
  if (engine.running) {
    list.push({ key: 'restart', label: t('databases.service.restart'), icon: RotateCw, onSelect: () => void serviceAction(engine, 'restart') })
    list.push({ key: 'stop', label: t('databases.service.stop'), icon: Square, onSelect: () => void serviceAction(engine, 'stop') })
  } else {
    list.push({ key: 'start', label: t('databases.service.start'), icon: Play, onSelect: () => void serviceAction(engine, 'start') })
  }
  list.push({
    key: 'remote',
    label: engine.remote ? t('databases.remote.localOnly') : t('databases.remote.enable'),
    icon: engine.remote ? Lock : Globe,
    onSelect: () => void toggleRemote(engine),
  })
  return list
}

function openSettings(engine: DbEngine) {
  softwareDrawer.open(lineKey(engine.software, engine.line))
}
</script>

<template>
  <nav :aria-label="t('databases.card.label')" class="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
    <section
      v-for="engine in engines"
      :key="engine.id"
      :class="
        cn(
          'relative flex min-w-0 flex-col gap-3 rounded-lg border bg-card px-4 py-3.5 transition-colors md:px-[18px]',
          engine.id === selected ? 'border-primary ring-1 ring-primary' : 'hover:border-muted-foreground/40',
        )
      "
    >
      <div class="flex items-start gap-3">
        <span
          class="inline-flex size-9 shrink-0 items-center justify-center rounded-lg bg-primary-soft text-primary-soft-foreground [&_svg]:size-[18px]"
          aria-hidden="true"
        >
          <Zap v-if="engine.kind === 'redis'" />
          <Database v-else />
        </span>
        <div class="flex min-w-0 flex-1 flex-col">
          <RouterLink
            :to="location(engine.id)"
            :aria-current="engine.id === selected ? 'page' : undefined"
            class="truncate rounded-sm font-semibold text-foreground no-underline outline-none after:absolute after:inset-0 after:rounded-lg focus-visible:ring-2 focus-visible:ring-ring/50"
          >
            {{ engineLabel(engine) }}
          </RouterLink>
          <span v-if="engine.version" class="truncate font-mono text-xs text-muted-foreground" dir="ltr">{{ engine.version }}</span>
        </div>
        <StatusDot :tone="engineTone(engine)" class="shrink-0 text-detail">{{ engineStateText(engine) }}</StatusDot>
      </div>
      <p class="m-0 text-detail text-muted-foreground tabular-nums">{{ meta(engine) }}</p>
      <p v-if="failure(engine)" class="m-0 text-xs text-destructive">{{ failure(engine) }}</p>
      <div class="relative z-10 flex flex-wrap items-center gap-2">
        <Button v-if="canOpenSoftware" variant="outline" size="sm" class="max-md:h-11" @click="openSettings(engine)">
          <Settings2 aria-hidden="true" />
          {{ t('databases.card.settings') }}
        </Button>
        <RowActions :actions="actions(engine)" :label="t('databases.card.more', { name: engineLabel(engine) })" />
      </div>
    </section>
  </nav>
</template>
