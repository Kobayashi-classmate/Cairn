<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Play, RefreshCw, RotateCw, Square, X, Zap } from '@lucide/vue'
import type { Offer, ServiceAction, SoftwareEntry } from '@/api/software'
import Card from '@/components/common/Card.vue'
import Notice from '@/components/common/Notice.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { Button } from '@/components/ui/button'
import { errorText } from '@/i18n/errors'
import { UNIT_TONE, lineKey, unitStateText } from '@/software/display'
import { useSoftwareStore } from '@/stores/software'
import { controlLine } from '../actions'

// The line's systemd services with their state. The buttons act on every
// service of the line at once; reloading is offered when all of them can.
const props = defineProps<{ entry: SoftwareEntry; offer: Offer; canManage: boolean }>()

const { t } = useI18n()
const store = useSoftwareStore()

const units = computed(() => props.offer.units ?? [])
const busy = computed(() => store.runningFor(props.entry.id) !== null)
const anyRunning = computed(() => units.value.some((unit) => unit.state === 'running'))
const allRunning = computed(() => units.value.length > 0 && units.value.every((unit) => unit.state === 'running'))
const canReload = computed(() => units.value.length > 0 && units.value.every((unit) => unit.canReload))

const failure = computed(() => store.serviceFailures[lineKey(props.entry.id, props.offer.line)] ?? null)
const failureText = computed(() => (failure.value ? (errorText(failure.value.code, failure.value.params ?? {}) ?? failure.value.code) : ''))
const failureOutput = computed(() => {
  const output = failure.value?.params?.output
  return typeof output === 'string' ? output.trim() : ''
})

function dismissFailure() {
  store.setServiceFailure(lineKey(props.entry.id, props.offer.line), null)
}

function run(action: ServiceAction) {
  void controlLine(props.entry, props.offer, action)
}
</script>

<template>
  <p class="m-0 text-muted-foreground">{{ t('software.service.intro') }}</p>

  <Card :title="t('software.service.units')" :level="3" flush>
    <template #actions>
      <Button variant="ghost" size="sm" :disabled="store.loading" @click="store.load()">
        <RefreshCw :class="store.loading ? 'animate-spin' : ''" aria-hidden="true" />
        {{ t('common.action.refresh') }}
      </Button>
    </template>
    <ul class="m-0 list-none divide-y p-0">
      <li v-for="unit in units" :key="unit.name" class="flex flex-wrap items-center gap-x-4 gap-y-1 px-4 py-3 md:px-[18px]">
        <span class="min-w-0 flex-1 font-mono text-[12.5px] [overflow-wrap:anywhere]" dir="ltr">{{ unit.name }}</span>
        <span v-if="unit.canReload" class="text-xs text-muted-foreground">{{ t('software.service.reloadable') }}</span>
        <StatusDot :tone="UNIT_TONE[unit.state]" class="w-28">{{ unitStateText(unit.state) }}</StatusDot>
      </li>
    </ul>
    <template v-if="canManage" #footer>
      <Button v-if="!allRunning" variant="outline" size="sm" class="max-md:h-11" :disabled="busy" @click="run('start')">
        <Play aria-hidden="true" />
        {{ t('software.service.start') }}
      </Button>
      <Button variant="outline" size="sm" class="max-md:h-11" :disabled="busy" @click="run('restart')">
        <RotateCw aria-hidden="true" />
        {{ t('software.service.restart') }}
      </Button>
      <Button v-if="canReload" variant="outline" size="sm" class="max-md:h-11" :disabled="busy || !allRunning" @click="run('reload')">
        <Zap aria-hidden="true" />
        {{ t('software.service.reload') }}
      </Button>
      <Button
        v-if="anyRunning"
        variant="outline"
        size="sm"
        class="max-md:h-11 text-destructive hover:text-destructive"
        :disabled="busy"
        @click="run('stop')"
      >
        <Square aria-hidden="true" />
        {{ t('software.service.stop') }}
      </Button>
    </template>
  </Card>

  <Notice v-if="busy" tone="info">{{ t('software.service.busy') }}</Notice>
  <div v-else-if="failure" role="alert" class="flex flex-col gap-2 rounded-lg bg-destructive-soft px-4 py-3 text-destructive">
    <div class="flex items-start gap-2 font-medium">
      <span class="flex-1">{{ failureText }}</span>
      <button
        type="button"
        class="inline-flex size-7 shrink-0 items-center justify-center rounded-sm opacity-80 outline-none hover:opacity-100 focus-visible:ring-2 focus-visible:ring-ring/50"
        :aria-label="t('common.action.dismiss')"
        @click="dismissFailure"
      >
        <X class="size-4" aria-hidden="true" />
      </button>
    </div>
    <pre
      v-if="failureOutput"
      tabindex="0"
      class="m-0 max-h-60 overflow-auto rounded-md bg-card p-3 font-mono text-xs whitespace-pre-wrap text-foreground [overflow-wrap:anywhere]"
      dir="ltr"
    >{{ failureOutput }}</pre>
  </div>
</template>
