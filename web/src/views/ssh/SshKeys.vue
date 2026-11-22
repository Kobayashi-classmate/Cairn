<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Plus, Trash2 } from '@lucide/vue'
import { isAbortError } from '@/api/client'
import { deleteSshKey, fetchSshKeys, type SshKey, type SshUser } from '@/api/ssh'
import DataTable from '@/components/common/DataTable.vue'
import Notice from '@/components/common/Notice.vue'
import SelectField from '@/components/common/SelectField.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import { Button } from '@/components/ui/button'
import { confirmAction } from '@/composables/useConfirmDialog'
import { notifySuccess } from '@/lib/notify'
import KeySheet from './KeySheet.vue'

// A user's authorized SSH keys: pick the user, then list, add and remove
// (step-up protected). Root's keys are read-only while root access is off.
const props = defineProps<{
  users: SshUser[]
  canManage: boolean
  /** False: root's keys cannot be changed (root access is off). */
  rootKeys: boolean
}>()
const emit = defineEmits<{ changed: [] }>()

const { t } = useI18n()
const user = ref('')
const keys = ref<SshKey[]>([])
const loading = ref(false)
const loadError = ref<unknown>(null)
const search = ref('')
const showAdd = ref(false)
let controller: AbortController | null = null

const userOptions = computed(() =>
  props.users.map((entry) => ({ value: entry.name, label: t('ssh.keys.userOption', { name: entry.name, count: entry.keys }, entry.keys) })),
)
const selected = computed(() => props.users.find((entry) => entry.name === user.value) ?? null)
/** Keys of this user can change. */
const editable = computed(() => props.canManage && !!selected.value && (!selected.value.root || props.rootKeys))

// Start with the first user that has keys, else the first one.
watch(
  () => props.users,
  (list) => {
    if (user.value && list.some((entry) => entry.name === user.value)) return
    user.value = (list.find((entry) => entry.keys > 0) ?? list[0])?.name ?? ''
  },
  { immediate: true },
)

async function load() {
  const name = user.value
  if (!name) return
  controller?.abort()
  const current = new AbortController()
  controller = current
  loading.value = true
  try {
    keys.value = await fetchSshKeys(name, current.signal)
    loadError.value = null
  } catch (err) {
    if (isAbortError(err)) return
    keys.value = []
    loadError.value = err
  } finally {
    if (controller === current) loading.value = false
  }
}

watch(
  user,
  () => {
    keys.value = []
    search.value = ''
    void load()
  },
  { immediate: true },
)
onBeforeUnmount(() => controller?.abort())

function changed() {
  void load()
  emit('changed')
}

function askDelete(key: SshKey) {
  const name = user.value
  void confirmAction({
    title: t('ssh.keys.deleteTitle', { user: name }),
    description: t('ssh.keys.deleteText', { fingerprint: key.fingerprint, user: name }),
    confirmLabel: t('ssh.keys.delete'),
    destructive: true,
    run: async () => {
      // Step-up protected: the client asks for the password and retries.
      await deleteSshKey(name, key.fingerprint)
      notifySuccess(t('ssh.keys.deleted'))
      changed()
    },
  })
}

const columns = computed<ColumnDef<SshKey>[]>(() => [
  { id: 'type', header: t('common.field.type'), accessorFn: (key) => key.type, meta: { class: 'w-[150px]' } },
  { id: 'fingerprint', header: t('ssh.keys.fingerprint'), accessorFn: (key) => key.fingerprint, meta: { class: 'min-w-[280px]' } },
  { id: 'comment', header: t('ssh.keys.comment'), accessorFn: (key) => key.comment.toLowerCase(), meta: { class: 'min-w-[160px]' } },
  { id: 'actions', header: '', enableSorting: false, meta: { class: 'w-[64px] text-end' } },
])
</script>

<template>
  <div class="flex flex-col gap-4">
    <Notice v-if="selected?.root && !rootKeys" tone="info">{{ t('ssh.keys.rootReadOnly') }}</Notice>

    <DataTable
      v-model:search="search"
      :data="keys"
      :columns="columns"
      :row-key="(key) => key.fingerprint"
      :label="t('ssh.keys.label', { user })"
      :loading="loading && keys.length === 0"
      :error="loadError"
      :search-placeholder="t('ssh.keys.search')"
      :search-text="(key) => [key.type, key.fingerprint, key.comment, key.options].join(' ')"
      :empty-title="users.length === 0 ? t('ssh.keys.noUsers') : t('ssh.keys.empty', { user })"
      :empty-text="users.length === 0 ? undefined : t('ssh.keys.emptyText')"
      :min-width="680"
      @retry="load"
    >
      <template #toolbar>
        <div class="flex items-center gap-2">
          <label for="ssh-keys-user" class="text-detail font-medium whitespace-nowrap">{{ t('common.field.user') }}</label>
          <SelectField id="ssh-keys-user" v-model="user" :options="userOptions" :disabled="users.length === 0" class="w-56 max-md:h-11" />
        </div>
        <Button v-if="editable" class="max-md:h-11" @click="showAdd = true">
          <Plus aria-hidden="true" />
          {{ t('ssh.keys.add') }}
        </Button>
      </template>
      <template #cell-type="{ row }">
        <span class="font-mono text-[12.5px]">{{ row.type }}</span>
      </template>
      <template #cell-fingerprint="{ row }">
        <span class="flex min-w-0 flex-col gap-0.5">
          <span class="font-mono text-[12.5px] [overflow-wrap:anywhere]" dir="ltr">{{ row.fingerprint }}</span>
          <span v-if="row.options" class="font-mono text-xs text-muted-foreground [overflow-wrap:anywhere]" dir="ltr">{{ row.options }}</span>
        </span>
      </template>
      <template #cell-comment="{ row }">
        <span class="[overflow-wrap:anywhere]" dir="auto">{{ row.comment }}</span>
      </template>
      <template #cell-actions="{ row }">
        <div v-if="editable" class="flex justify-end">
          <Button
            variant="ghost"
            size="icon"
            class="text-destructive hover:bg-destructive-soft hover:text-destructive max-md:size-11"
            :aria-label="t('ssh.keys.deleteOne', { fingerprint: row.fingerprint })"
            :title="t('ssh.keys.delete')"
            @click="askDelete(row)"
          >
            <Trash2 aria-hidden="true" />
          </Button>
        </div>
      </template>
      <template v-if="editable" #empty>
        <Button @click="showAdd = true">
          <Plus aria-hidden="true" />
          {{ t('ssh.keys.add') }}
        </Button>
      </template>
    </DataTable>

    <KeySheet v-if="user" v-model:open="showAdd" :user="user" @added="changed" />
  </div>
</template>
