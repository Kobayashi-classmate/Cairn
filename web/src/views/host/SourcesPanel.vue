<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RefreshCw, RotateCcw } from '@lucide/vue'
import { fetchSourceBackups, fetchSystemSources, restoreSources, switchSystemSource, type SourceBackup } from '@/api/host'
import type { Job } from '@/api/jobs'
import type { Repository, SourceCandidate, SourceSlot } from '@/api/software'
import SourceSlots from '@/components/SourceSlots.vue'
import Card from '@/components/common/Card.vue'
import DataTable from '@/components/common/DataTable.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { confirmAction } from '@/composables/useConfirmDialog'
import { launchJob } from '@/jobs/dock'

// The distribution's package sources: compare the mirrors and switch the
// main or security source; earlier files are backed up and can come back.
// Comparing takes a while, so it only runs when the tab is first opened or
// when asked.
const props = defineProps<{ canManage: boolean }>()

const { t } = useI18n()
const slots = ref<SourceSlot[] | null>(null)
const probing = ref(false)
const probeError = ref<unknown>(null)
const backups = ref<SourceBackup[] | null>(null)
const backupsError = ref<unknown>(null)
/** A switch or restore job runs. */
const busy = ref(false)
/** The slots component wants a selection; system sources have none. */
const noSelection = ref<Record<string, string>>({})
const noRepos: ReadonlyMap<string, Repository> = new Map()

async function probe() {
  probing.value = true
  try {
    slots.value = await fetchSystemSources()
    probeError.value = null
  } catch (err) {
    probeError.value = err
  } finally {
    probing.value = false
  }
}

async function loadBackups() {
  try {
    backups.value = await fetchSourceBackups()
    backupsError.value = null
  } catch (err) {
    backupsError.value = err
  }
}

onMounted(() => {
  void probe()
  void loadBackups()
})

async function run(start: () => Promise<Job>) {
  busy.value = true
  try {
    // Step-up protected: the client asks for the password and retries.
    const job = await launchJob(start, {
      rethrow: true,
      onFinished: () => {
        busy.value = false
        void probe()
        void loadBackups()
      },
    })
    if (!job) busy.value = false
  } catch (err) {
    busy.value = false
    throw err
  }
}

function switchTo(slot: SourceSlot, candidate: SourceCandidate) {
  const role = slot.role ?? 'main'
  void confirmAction({
    title: t('host.sources.switchTitle', { role: t(`host.sources.role.${role}`) }),
    description: t('host.sources.switchText', { name: candidate.name }),
    confirmLabel: t('host.sources.switch'),
    run: () => run(() => switchSystemSource(role, candidate.id)),
  })
}

function restore(backup: SourceBackup) {
  void confirmAction({
    title: t('host.sources.restoreTitle'),
    description: t('host.sources.restoreText', { from: backup.from, to: backup.to }),
    confirmLabel: t('host.sources.restore'),
    run: () => run(() => restoreSources(backup.id)),
  })
}

const columns = computed<ColumnDef<SourceBackup>[]>(() => [
  { id: 'at', header: t('host.sources.backupAt'), accessorFn: (backup) => backup.at, meta: { class: 'w-[150px]' } },
  { id: 'role', header: t('common.field.role'), accessorFn: (backup) => backup.role, meta: { class: 'w-[120px]' } },
  { id: 'change', header: t('host.sources.change'), enableSorting: false, meta: { class: 'min-w-[260px]' } },
  { id: 'actions', header: '', enableSorting: false, meta: { class: 'w-[120px] text-end' } },
])
</script>

<template>
  <div class="flex flex-col gap-4">
    <Card :title="t('host.sources.title')" :description="t('host.sources.intro')">
      <template #actions>
        <Button variant="outline" size="sm" class="max-md:h-11" :disabled="probing" @click="probe">
          <RefreshCw :class="probing ? 'animate-spin' : ''" aria-hidden="true" />
          {{ t('host.sources.compare') }}
        </Button>
      </template>
      <div v-if="probing && !slots" class="flex flex-col gap-3" role="status" aria-busy="true">
        <p class="m-0 text-detail text-muted-foreground">{{ t('host.sources.probing') }}</p>
        <Skeleton class="h-24 w-full rounded-lg" />
        <Skeleton class="h-24 w-full rounded-lg" />
      </div>
      <ErrorState v-else-if="probeError && !slots" :error="probeError" compact @retry="probe" />
      <template v-else-if="slots">
        <p v-if="probing" class="m-0 mb-3 text-detail text-muted-foreground" role="status">{{ t('host.sources.probing') }}</p>
        <SourceSlots
          v-if="slots.length > 0"
          v-model:selection="noSelection"
          :slots="slots"
          :repos="noRepos"
          :switchable="canManage"
          :switch-disabled="busy || probing"
          :heading-level="3"
          @switch="switchTo"
        />
        <EmptyState v-else compact :title="t('errors.syssrc.unsupported')" />
      </template>
    </Card>

    <section class="flex flex-col gap-2" aria-labelledby="host-source-backups">
      <h2 id="host-source-backups" class="m-0 text-section">{{ t('host.sources.backups') }}</h2>
      <DataTable
        :data="backups ?? []"
        :columns="columns"
        :row-key="(backup) => backup.id"
        :label="t('host.sources.backups')"
        :loading="backups === null && !backupsError"
        :error="backupsError"
        :empty-title="t('host.sources.noBackups')"
        :min-width="660"
        :initial-sort="[{ id: 'at', desc: true }]"
        @retry="loadBackups"
      >
        <template #cell-at="{ row }"><RelativeTime :value="row.at" /></template>
        <template #cell-role="{ row }">{{ t(`host.sources.role.${row.role}`) }}</template>
        <template #cell-change="{ row }">
          <span class="flex min-w-0 flex-col gap-0.5">
            <span class="font-mono text-[12.5px] [overflow-wrap:anywhere]" dir="ltr">{{ row.from }} → {{ row.to }}</span>
            <span class="font-mono text-xs text-muted-foreground [overflow-wrap:anywhere]" dir="ltr">{{ row.files.join(', ') }}</span>
          </span>
        </template>
        <template #cell-actions="{ row }">
          <div v-if="props.canManage" class="flex justify-end">
            <Button variant="outline" size="sm" class="max-md:h-11" :disabled="busy" @click="restore(row)">
              <RotateCcw aria-hidden="true" />
              {{ t('host.sources.restore') }}
            </Button>
          </div>
        </template>
      </DataTable>
    </section>
  </div>
</template>
