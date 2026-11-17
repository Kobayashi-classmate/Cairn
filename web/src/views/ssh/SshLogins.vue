<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Recent SSH logins, accepted and failed, newest first.
import { computed, h, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NDataTable, NTag, type DataTableColumns } from 'naive-ui'
import { fetchSshLogins, type SshLogin } from '@/api/ssh'
import { errorMessage } from '@/i18n/errors'
import { formatDateTime } from '@/i18n/format'

const PAGE = 100

const { t, te } = useI18n()
const entries = ref<SshLogin[]>([])
const next = ref('')
const loading = ref(false)
const loadError = ref<unknown>(null)

async function load(more = false) {
  loading.value = true
  try {
    const page = await fetchSshLogins(PAGE, more ? next.value : undefined)
    entries.value = more ? entries.value.concat(page.entries) : page.entries
    next.value = page.next
    loadError.value = null
  } catch (err) {
    loadError.value = err
  } finally {
    loading.value = false
  }
}

onMounted(() => void load())

function label(prefix: string, value: string): string {
  const key = `${prefix}.${value}`
  return value && te(key) ? t(key) : value
}

const columns = computed<DataTableColumns<SshLogin>>(() => [
  { key: 'time', title: t('ssh.logins.time'), width: 170, render: (entry) => formatDateTime(entry.time) },
  { key: 'user', title: t('common.field.user'), width: 140, render: (entry) => h('span', { class: 'mono' }, entry.user) },
  { key: 'ip', title: t('ssh.logins.ip'), minWidth: 160, render: (entry) => h('span', { class: 'mono' }, entry.ip) },
  { key: 'method', title: t('ssh.logins.method'), width: 160, render: (entry) => label('ssh.method', entry.method) || '-' },
  {
    key: 'result',
    title: t('ssh.logins.result'),
    width: 130,
    render: (entry) =>
      h(NTag, { size: 'small', type: entry.result === 'accepted' ? 'success' : 'error', bordered: false }, () => label('ssh.result', entry.result)),
  },
])
</script>

<template>
  <div>
    <div class="toolbar">
      <NButton size="small" :loading="loading" @click="load()">{{ t('common.action.refresh') }}</NButton>
    </div>
    <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>
    <NDataTable :columns="columns" :data="entries" :loading="loading" :scroll-x="720" size="small" />
    <div v-if="next" class="more">
      <NButton size="small" :loading="loading" @click="load(true)">{{ t('common.action.loadMore') }}</NButton>
    </div>
  </div>
</template>

<style scoped>
.toolbar {
  display: flex;
  justify-content: flex-end;
  margin-block-end: 8px;
}

.block {
  margin-block-end: 8px;
}

.more {
  display: flex;
  justify-content: center;
  margin-block-start: 8px;
}

:deep(.mono) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
}
</style>
