<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Lock, Pencil, Plus, Trash2 } from '@lucide/vue'
import { addRule, deleteRule, type ChangeResult, type FirewallRule } from '@/api/firewall'
import DataTable from '@/components/common/DataTable.vue'
import RowActions from '@/components/common/RowActions.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import type { RowAction } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { confirmAction } from '@/composables/useConfirmDialog'
import type { ChangeInfo } from '@/composables/useFirewallConfirm'
import { describeRule, protocolLabel, ruleRequest } from './labels'
import { isProtected, ownerLabel } from './owners'

// The port rules: what each allows or refuses, from where, and who made it.
// The rules that keep SSH and the panel reachable carry a lock and cannot
// change; rules of other features only take new sources.
const props = defineProps<{
  rules: readonly FirewallRule[]
  loading: boolean
  error: unknown
  canManage: boolean
  /** Changes are possible right now (firewall on, nothing pending). */
  writable: boolean
}>()

const emit = defineEmits<{
  create: []
  edit: [rule: FirewallRule]
  changed: [result: ChangeResult, info: ChangeInfo]
  retry: []
}>()

const { t } = useI18n()
const search = ref('')

const columns = computed<ColumnDef<FirewallRule>[]>(() => [
  { id: 'action', header: t('common.field.action'), accessorFn: (rule) => rule.action, meta: { class: 'w-[100px]' } },
  { id: 'protocol', header: t('common.field.protocol'), accessorFn: (rule) => rule.protocol, meta: { class: 'w-[120px]' } },
  { id: 'ports', header: t('common.field.ports'), accessorFn: (rule) => Number.parseInt(rule.ports[0] ?? '0', 10), meta: { class: 'min-w-[140px]' } },
  { id: 'sources', header: t('common.field.sources'), enableSorting: false, meta: { class: 'min-w-[180px]' } },
  { id: 'note', header: t('common.field.note'), accessorFn: (rule) => rule.note.toLowerCase(), meta: { class: 'min-w-[140px]' } },
  { id: 'owner', header: t('firewall.rule.owner'), accessorFn: (rule) => ownerLabel(rule.owner), meta: { class: 'w-[200px]' } },
  { id: 'actions', header: '', enableSorting: false, meta: { class: 'w-[100px] text-end' } },
])

function searchText(rule: FirewallRule): string {
  return [t(`firewall.action.${rule.action}`), protocolLabel(rule.protocol), ...rule.ports, ...rule.sources, rule.note, ownerLabel(rule.owner)].join(' ')
}

/** Rules the user can change here: not the protected ones. */
function changeable(rule: FirewallRule): boolean {
  return props.canManage && !isProtected(rule.owner)
}

function askDelete(rule: FirewallRule) {
  const ports = rule.ports.join(', ')
  void confirmAction({
    title: t('firewall.rule.deleteTitle', { ports }),
    description: t('firewall.rule.deleteText', { ports }),
    confirmLabel: t('firewall.rule.delete'),
    destructive: true,
    run: async () => {
      // Step-up protected: the client asks for the password and retries.
      const result = await deleteRule(rule.id)
      const previous = ruleRequest(rule)
      emit('changed', result, {
        summary: t('firewall.summary.ruleDelete', { rule: describeRule(rule) }),
        undo: () => addRule(previous),
      })
    },
  })
}

function actions(rule: FirewallRule): RowAction[] {
  // Rules of other features are removed by the feature, not here.
  if (rule.owner) return []
  return [{ key: 'delete', label: t('firewall.rule.delete'), icon: Trash2, destructive: true, disabled: !props.writable, onSelect: () => askDelete(rule) }]
}
</script>

<template>
  <DataTable
    v-model:search="search"
    :data="rules"
    :columns="columns"
    :row-key="(rule) => rule.id"
    :label="t('firewall.tabs.rules')"
    :loading="loading"
    :error="error"
    :search-placeholder="t('firewall.rule.search')"
    :search-text="searchText"
    :empty-title="t('firewall.rule.empty')"
    :min-width="960"
    @retry="emit('retry')"
  >
    <template #cell-action="{ row }">
      <StatusPill :tone="row.action === 'allow' ? 'success' : 'destructive'">{{ t(`firewall.action.${row.action}`) }}</StatusPill>
    </template>
    <template #cell-protocol="{ row }">{{ protocolLabel(row.protocol) }}</template>
    <template #cell-ports="{ row }">
      <span class="font-mono text-[12.5px] [overflow-wrap:anywhere]" dir="ltr">{{ row.ports.join(', ') }}</span>
    </template>
    <template #cell-sources="{ row }">
      <span v-if="row.sources.length === 0" class="text-muted-foreground">{{ t('firewall.anywhere') }}</span>
      <span v-else class="flex flex-wrap gap-1">
        <span v-for="source in row.sources" :key="source" class="rounded-sm bg-muted px-1.5 font-mono text-[12.5px]" dir="ltr">{{ source }}</span>
      </span>
    </template>
    <template #cell-note="{ row }">
      <span class="[overflow-wrap:anywhere]">{{ row.note }}</span>
    </template>
    <template #cell-owner="{ row }">
      <Tooltip v-if="isProtected(row.owner)">
        <TooltipTrigger as-child>
          <span class="inline-flex items-center gap-1.5 text-muted-foreground" tabindex="0">
            <Lock class="size-3.5 shrink-0" aria-hidden="true" />
            {{ ownerLabel(row.owner) }}
            <span class="sr-only">{{ t('firewall.rule.protected') }}</span>
          </span>
        </TooltipTrigger>
        <TooltipContent>{{ t('firewall.rule.protected') }}</TooltipContent>
      </Tooltip>
      <StatusPill v-else-if="row.owner" tone="progress">{{ ownerLabel(row.owner) }}</StatusPill>
    </template>
    <template #cell-actions="{ row }">
      <div v-if="changeable(row)" class="flex justify-end gap-2">
        <Button
          variant="outline"
          size="icon"
          class="max-md:size-11"
          :disabled="!writable"
          :aria-label="row.owner ? t('firewall.rule.editSources', { ports: row.ports.join(', ') }) : t('firewall.rule.edit', { ports: row.ports.join(', ') })"
          :title="row.owner ? t('firewall.rule.editSources', { ports: row.ports.join(', ') }) : t('firewall.rule.edit', { ports: row.ports.join(', ') })"
          @click="emit('edit', row)"
        >
          <Pencil aria-hidden="true" />
        </Button>
        <RowActions :actions="actions(row)" :label="t('firewall.rule.more', { ports: row.ports.join(', ') })" />
      </div>
    </template>
    <template v-if="canManage && writable" #empty>
      <Button @click="emit('create')">
        <Plus aria-hidden="true" />
        {{ t('firewall.rule.add') }}
      </Button>
    </template>
  </DataTable>
</template>
