<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// A user's authorized SSH keys: list, add and remove (step-up protected).
import { computed, h, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NDataTable, NEmpty, NInput, NSelect, NText, useMessage, type DataTableColumns } from 'naive-ui'
import { isAbortError, isStepUpRequired } from '@/api/client'
import { addSshKey, deleteSshKey, fetchSshKeys, type SshKey, type SshUser } from '@/api/ssh'
import { useConfirm } from '@/composables/useConfirm'
import { useErrorToast } from '@/composables/useErrorToast'
import { errorMessage } from '@/i18n/errors'

const props = defineProps<{
  users: SshUser[]
  canManage: boolean
  /** False: root's keys cannot be changed (root access is off). */
  rootKeys: boolean
}>()
const emit = defineEmits<{ changed: [] }>()

/** Public keys start with their type: ssh-ed25519, ecdsa-sha2-…, ssh-rsa, sk-… */
const KEY_PATTERN = /^(?:\S+\s+)?(ssh-(ed25519|rsa|dss)|ecdsa-sha2-\S+|sk-\S+)\s+[A-Za-z0-9+/=]+(\s.*)?$/

const { t } = useI18n()
const message = useMessage()
const confirm = useConfirm()
const showError = useErrorToast()
const user = ref<string | null>(null)
const keys = ref<SshKey[]>([])
const loading = ref(false)
const loadError = ref<unknown>(null)
const newKey = ref('')
const adding = ref(false)
const addError = ref<unknown>(null)
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
    user.value = (list.find((entry) => entry.keys > 0) ?? list[0])?.name ?? null
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

watch(user, () => {
  newKey.value = ''
  addError.value = null
  void load()
}, { immediate: true })

const keyValid = computed(() => KEY_PATTERN.test(newKey.value.trim()))

async function add() {
  const name = user.value
  if (!name || !keyValid.value) return
  adding.value = true
  addError.value = null
  try {
    // Step-up protected: the client asks for the password and retries.
    await addSshKey(name, newKey.value.trim())
    newKey.value = ''
    message.success(t('ssh.keys.added'))
    await load()
    emit('changed')
  } catch (err) {
    if (!isStepUpRequired(err)) addError.value = err
  } finally {
    adding.value = false
  }
}

async function remove(key: SshKey) {
  const name = user.value
  if (!name) return
  const ok = await confirm({
    title: t('ssh.keys.deleteTitle'),
    content: t('ssh.keys.deleteText', { fingerprint: key.fingerprint, user: name }),
    confirmText: t('common.action.delete'),
    danger: true,
  })
  if (!ok) return
  try {
    await deleteSshKey(name, key.fingerprint)
    await load()
    emit('changed')
  } catch (err) {
    if (!isStepUpRequired(err)) showError(err)
  }
}

const columns = computed<DataTableColumns<SshKey>>(() => [
  { key: 'type', title: t('common.field.type'), width: 140, render: (key) => h('span', { class: 'mono' }, key.type) },
  {
    key: 'fingerprint',
    title: t('ssh.keys.fingerprint'),
    minWidth: 260,
    render: (key) =>
      h('div', { class: 'cell' }, [
        h('span', { class: 'mono' }, key.fingerprint),
        key.options ? h(NText, { depth: 3, class: 'cell__sub mono' }, () => key.options) : null,
      ]),
  },
  { key: 'comment', title: t('ssh.keys.comment'), minWidth: 160, render: (key) => key.comment || '' },
  {
    key: 'actions',
    title: '',
    width: 90,
    render: (key) =>
      editable.value
        ? h(NButton, { size: 'small', quaternary: true, type: 'error', onClick: () => void remove(key) }, () => t('common.action.delete'))
        : null,
  },
])
</script>

<template>
  <div class="keys">
    <div class="keys__head">
      <NSelect v-model:value="user" :options="userOptions" filterable class="keys__user" :aria-label="t('common.field.user')" />
      <NText v-if="selected?.root && !rootKeys" depth="3" class="hint">{{ t('ssh.keys.rootReadOnly') }}</NText>
    </div>
    <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>
    <NEmpty v-else-if="!loading && keys.length === 0" :description="t('ssh.keys.empty')" class="empty" />
    <NDataTable
      v-else
      :columns="columns"
      :data="keys"
      :row-key="(key: SshKey) => key.fingerprint"
      :loading="loading"
      :scroll-x="640"
      size="small"
    />
    <div v-if="editable" class="add">
      <NInput
        v-model:value="newKey"
        type="textarea"
        :autosize="{ minRows: 2, maxRows: 5 }"
        class="mono"
        :placeholder="t('ssh.keys.placeholder')"
        :status="newKey.trim() && !keyValid ? 'error' : undefined"
        :input-props="{ spellcheck: false, autocomplete: 'off' }"
      />
      <NText v-if="newKey.trim() && !keyValid" type="error" class="hint">{{ t('errors.ssh.key_invalid') }}</NText>
      <NAlert v-if="addError" type="error" class="block">{{ errorMessage(addError) }}</NAlert>
      <div class="add__actions">
        <NButton type="primary" :loading="adding" :disabled="!keyValid" @click="add">{{ t('ssh.keys.add') }}</NButton>
      </div>
    </div>
  </div>
</template>

<style scoped>
.keys__head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 12px;
  margin-block-end: 12px;
}

.keys__user {
  inline-size: 260px;
}

.hint {
  font-size: 12px;
}

.block {
  margin-block: 8px;
}

.empty {
  padding-block: 24px;
}

.add {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-block-start: 12px;
}

.add__actions {
  display: flex;
  justify-content: flex-end;
}

.mono :deep(textarea) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
}

:deep(.cell) {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

:deep(.cell__sub) {
  font-size: 11px;
  overflow-wrap: anywhere;
}

:deep(.mono) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  overflow-wrap: anywhere;
}
</style>
