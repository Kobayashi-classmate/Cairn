<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { I18nT, useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { Plus } from '@lucide/vue'
import { fetchTokens, revokeToken, type ApiToken, type CreatedToken } from '@/api/tokens'
import Card from '@/components/common/Card.vue'
import DataTable from '@/components/common/DataTable.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import type { ColumnDef } from '@/components/common/dataTable'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { permissionLabel } from '@/auth/permissions'
import { useClock } from '@/composables/useClock'
import { formatDate, formatFullDateTime } from '@/i18n/format'
import { confirmAction } from '@/composables/useConfirmDialog'
import { notifySuccess } from '@/lib/notify'
import CreateTokenSheet from '@/views/tokens/CreateTokenSheet.vue'
import TokenCreatedDialog from '@/views/tokens/TokenCreatedDialog.vue'

// Your API tokens: what each may do, from where, until when and when it was
// last used. Creating one shows its secret once; revoking asks first.
const { t } = useI18n()
const now = useClock()

/** Permissions listed by name in a row; the rest are counted. */
const SHOWN_PERMISSIONS = 2

const tokens = ref<ApiToken[]>([])
const loading = ref(false)
const loadError = ref<unknown>(null)
const showCreate = ref(false)
const created = ref<{ name: string; secret: string } | null>(null)

async function load() {
  loading.value = true
  try {
    tokens.value = await fetchTokens()
    loadError.value = null
  } catch (err) {
    loadError.value = err
  } finally {
    loading.value = false
  }
}

onMounted(load)

function onCreated(token: CreatedToken) {
  tokens.value = [token.info, ...tokens.value.filter((item) => item.id !== token.info.id)]
  created.value = { name: token.info.name, secret: token.token }
}

function askRevoke(token: ApiToken) {
  void confirmAction({
    title: t('tokens.confirmRevoke.title', { name: token.name }),
    description: t('tokens.confirmRevoke.content'),
    confirmLabel: t('tokens.revoke'),
    destructive: true,
    run: async () => {
      await revokeToken(token.id)
      tokens.value = tokens.value.filter((item) => item.id !== token.id)
      notifySuccess(t('tokens.revoked', { name: token.name }))
    },
  })
}

function expired(token: ApiToken): boolean {
  return token.expiresAt > 0 && token.expiresAt <= now.value
}

const columns = computed<ColumnDef<ApiToken>[]>(() => [
  { id: 'name', header: t('common.field.name'), accessorFn: (token) => token.name.toLowerCase(), meta: { class: 'min-w-[170px]' } },
  { id: 'permissions', header: t('common.field.permissions'), enableSorting: false, meta: { class: 'min-w-[170px]' } },
  { id: 'allowedIps', header: t('common.field.allowedIps'), enableSorting: false, meta: { class: 'min-w-[130px]' } },
  { id: 'expires', header: t('tokens.column.expires'), accessorFn: (token) => token.expiresAt || Number.MAX_SAFE_INTEGER, meta: { class: 'w-[130px]' } },
  { id: 'lastUsed', header: t('tokens.column.lastUsed'), accessorFn: (token) => token.lastUsedAt, meta: { class: 'w-[130px]' } },
  { id: 'actions', header: '', enableSorting: false, meta: { class: 'w-[100px] text-end' } },
])
</script>

<template>
  <Card :title="t('tokens.listTitle')" :level="3" flush>
    <template #actions>
      <Button size="sm" class="max-md:h-11" @click="showCreate = true">
        <Plus aria-hidden="true" />
        {{ t('tokens.create.title') }}
      </Button>
    </template>
    <div class="flex flex-col gap-1 border-b px-4 py-3 text-detail text-muted-foreground md:px-[18px]">
      <I18nT keypath="tokens.help.usage" tag="p" class="m-0">
        <template #header>
          <code class="rounded-sm bg-muted px-1 font-mono text-[12.5px] text-foreground" dir="ltr">Authorization: Bearer &lt;token&gt;</code>
        </template>
      </I18nT>
      <p class="m-0">{{ t('tokens.help.stepUp') }} {{ t('tokens.help.audit') }}</p>
      <p class="m-0">
        <RouterLink :to="{ name: 'settings-api' }" class="text-link underline-offset-2 hover:underline">
          {{ t('tokens.help.reference') }}
        </RouterLink>
      </p>
    </div>
    <DataTable
      flat
      :data="tokens"
      :columns="columns"
      :row-key="(token) => String(token.id)"
      :label="t('tokens.title')"
      :loading="loading"
      :error="loadError"
      :empty-title="t('tokens.empty')"
      :empty-text="t('tokens.emptyText')"
      :min-width="760"
      :skeleton-rows="2"
      @retry="load"
    >
      <template #cell-name="{ row }">
        <span class="flex min-w-0 flex-col">
          <span class="truncate font-medium" dir="auto">{{ row.name }}</span>
          <span class="font-mono text-xs text-muted-foreground" dir="ltr">{{ row.prefix }}…</span>
          <span class="text-xs text-muted-foreground" :title="formatFullDateTime(row.createdAt)">
            {{ t('tokens.createdOn', { date: formatDate(row.createdAt) }) }}
          </span>
        </span>
      </template>
      <template #cell-permissions="{ row }">
        <span class="flex flex-wrap items-center gap-1">
          <StatusPill v-for="permission in row.permissions.slice(0, SHOWN_PERMISSIONS)" :key="permission" tone="progress">
            {{ permissionLabel(permission) }}
          </StatusPill>
          <Tooltip v-if="row.permissions.length > SHOWN_PERMISSIONS">
            <TooltipTrigger as-child>
              <button type="button" class="rounded-full outline-none focus-visible:ring-2 focus-visible:ring-ring/50">
                <StatusPill>{{ t('tokens.morePermissions', { count: row.permissions.length - SHOWN_PERMISSIONS }) }}</StatusPill>
              </button>
            </TooltipTrigger>
            <TooltipContent class="max-w-72">
              {{ row.permissions.slice(SHOWN_PERMISSIONS).map(permissionLabel).join(', ') }}
            </TooltipContent>
          </Tooltip>
        </span>
      </template>
      <template #cell-allowedIps="{ row }">
        <span v-if="row.allowedIps.length > 0" class="flex flex-col font-mono text-[12.5px]" dir="ltr">
          <span v-for="ip in row.allowedIps" :key="ip" class="whitespace-nowrap">{{ ip }}</span>
        </span>
        <span v-else class="text-muted-foreground">{{ t('tokens.anyAddress') }}</span>
      </template>
      <template #cell-expires="{ row }">
        <span v-if="row.expiresAt === 0" class="text-muted-foreground">{{ t('tokens.expiry.never') }}</span>
        <span v-else class="flex flex-col items-start gap-0.5">
          <StatusPill v-if="expired(row)" tone="destructive">{{ t('tokens.expired') }}</StatusPill>
          <time
            :datetime="new Date(row.expiresAt * 1000).toISOString()"
            :title="formatFullDateTime(row.expiresAt)"
            :class="['whitespace-nowrap', expired(row) && 'text-xs text-muted-foreground']"
          >
            {{ formatDate(row.expiresAt) }}
          </time>
        </span>
      </template>
      <template #cell-lastUsed="{ row }">
        <RelativeTime v-if="row.lastUsedAt > 0" :value="row.lastUsedAt" />
        <span v-else class="text-muted-foreground">{{ t('tokens.neverUsed') }}</span>
      </template>
      <template #cell-actions="{ row }">
        <Button
          variant="outline"
          size="sm"
          class="text-destructive hover:text-destructive max-md:h-11"
          :aria-label="t('tokens.revokeNamed', { name: row.name })"
          @click="askRevoke(row)"
        >
          {{ t('tokens.revoke') }}
        </Button>
      </template>
      <template #empty>
        <Button @click="showCreate = true">
          <Plus aria-hidden="true" />
          {{ t('tokens.create.title') }}
        </Button>
      </template>
    </DataTable>
  </Card>

  <CreateTokenSheet v-model:open="showCreate" @created="onCreated" />
  <TokenCreatedDialog :token="created" @close="created = null" />
</template>
