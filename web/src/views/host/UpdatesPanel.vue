<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { Download, RefreshCw, ShieldCheck } from '@lucide/vue'
import { checkUpdates, installUpdates, setAutoUpdates, type HostUpdate, type HostView } from '@/api/host'
import type { Job } from '@/api/jobs'
import Card from '@/components/common/Card.vue'
import DataTable from '@/components/common/DataTable.vue'
import Notice from '@/components/common/Notice.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { confirmAction } from '@/composables/useConfirmDialog'
import { launchJob } from '@/jobs/dock'

// Package updates of the system: check, install (all or security only) and
// automatic security updates. Each is a job in the job dock.
const props = defineProps<{ view: HostView; canManage: boolean }>()
const emit = defineEmits<{ changed: [] }>()

const { t } = useI18n()
const search = ref('')
/** An update job runs; the buttons wait for it. */
const busy = ref(false)

const updates = computed(() => props.view.updates)
const securityCount = computed(() => updates.value.packages.filter((pkg) => pkg.security).length)
/** Sources that hold updates back, from the last check. */
const sourceWarnings = computed(() => updates.value.sources.filter((source) => source.warning))

async function run(start: () => Promise<Job>, rethrow = false) {
  busy.value = true
  try {
    const job = await launchJob(start, {
      rethrow,
      onFinished: () => {
        busy.value = false
        emit('changed')
      },
    })
    if (!job) busy.value = false
  } catch (err) {
    busy.value = false
    throw err
  }
}

function check() {
  void run(checkUpdates)
}

function install(securityOnly: boolean) {
  const count = securityOnly ? securityCount.value : updates.value.packages.length
  void confirmAction({
    title: securityOnly ? t('host.updates.installSecurityTitle', { count }, count) : t('host.updates.installAllTitle', { count }, count),
    description: t('host.updates.installText'),
    confirmLabel: securityOnly ? t('host.updates.installSecurityShort') : t('host.updates.installAllShort'),
    // Step-up protected: the client asks for the password and retries.
    run: () => run(() => installUpdates(securityOnly), true),
  })
}

function toggleAuto(enabled: boolean) {
  void run(() => setAutoUpdates(enabled))
}

const columns = computed<ColumnDef<HostUpdate>[]>(() => [
  { id: 'name', header: t('common.field.name'), accessorFn: (pkg) => pkg.name, meta: { class: 'min-w-[220px]' } },
  { id: 'from', header: t('host.updates.from'), accessorFn: (pkg) => pkg.from, enableSorting: false, meta: { class: 'min-w-[160px]' } },
  { id: 'to', header: t('host.updates.to'), accessorFn: (pkg) => pkg.to, enableSorting: false, meta: { class: 'min-w-[160px]' } },
])
</script>

<template>
  <div class="flex flex-col gap-4">
    <Notice v-for="source in sourceWarnings" :key="source.role" tone="warning">
      {{ t(`host.updates.sourceWarning.${source.warning}`, { role: t(`host.sources.role.${source.role}`) }) }}
      <template #action>
        <RouterLink :to="{ query: { tab: 'sources' } }" class="underline underline-offset-2 hover:no-underline">
          {{ t('host.updates.openSources') }}
        </RouterLink>
      </template>
    </Notice>

    <Card flush>
      <template #title>
        <span class="font-normal">
          <template v-if="updates.checkedAt">
            {{ t('host.updates.checked') }} <RelativeTime :value="updates.checkedAt" />
          </template>
          <template v-else>{{ t('host.updates.never') }}</template>
        </span>
      </template>
      <template v-if="canManage" #actions>
        <Button variant="outline" size="sm" class="max-md:h-11" :disabled="busy" @click="check">
          <RefreshCw aria-hidden="true" />
          {{ t('host.updates.check') }}
        </Button>
        <Button variant="outline" size="sm" class="max-md:h-11" :disabled="busy || securityCount === 0" @click="install(true)">
          <ShieldCheck aria-hidden="true" />
          {{ t('host.updates.installSecurity', { count: securityCount }) }}
        </Button>
        <Button size="sm" class="max-md:h-11" :disabled="busy || updates.packages.length === 0" @click="install(false)">
          <Download aria-hidden="true" />
          {{ t('host.updates.installAll', { count: updates.packages.length }) }}
        </Button>
      </template>
      <div class="flex flex-col gap-1 px-4 py-3.5 md:px-[18px]">
        <div class="flex flex-wrap items-center gap-3">
          <Switch
            id="host-auto-updates"
            :model-value="view.autoUpdates.enabled"
            :disabled="!canManage || !view.autoUpdates.available || busy"
            aria-describedby="host-auto-updates-hint"
            @update:model-value="toggleAuto"
          />
          <label for="host-auto-updates" class="cursor-pointer text-detail font-medium">{{ t('host.updates.auto') }}</label>
        </div>
        <p id="host-auto-updates-hint" class="m-0 text-xs text-muted-foreground">
          {{ view.autoUpdates.available ? t('host.updates.autoHint', { tool: view.autoUpdates.tool }) : t('errors.host.updates_unavailable') }}
        </p>
      </div>
    </Card>

    <DataTable
      v-model:search="search"
      :data="updates.packages"
      :columns="columns"
      :row-key="(pkg) => pkg.name"
      :label="t('host.tabs.updates')"
      :search-placeholder="t('host.updates.search')"
      :search-text="(pkg) => [pkg.name, pkg.from, pkg.to].join(' ')"
      :empty-title="updates.checkedAt ? t('host.updates.none') : t('host.updates.never')"
      :empty-text="updates.checkedAt ? undefined : t('host.updates.neverText')"
      :min-width="600"
      :initial-sort="[{ id: 'name', desc: false }]"
    >
      <template #cell-name="{ row }">
        <span class="inline-flex flex-wrap items-center gap-2">
          <span class="font-mono text-[12.5px]">{{ row.name }}</span>
          <StatusPill v-if="row.security" tone="warning">{{ t('host.updates.security') }}</StatusPill>
        </span>
      </template>
      <template #cell-from="{ row }"><span class="font-mono text-[12.5px] [overflow-wrap:anywhere]">{{ row.from }}</span></template>
      <template #cell-to="{ row }"><span class="font-mono text-[12.5px] [overflow-wrap:anywhere]">{{ row.to }}</span></template>
    </DataTable>
  </div>
</template>
