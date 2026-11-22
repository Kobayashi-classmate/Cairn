<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, type RouteLocationRaw } from 'vue-router'
import { X } from '@lucide/vue'
import type { AuditEntry } from '@/api/audit'
import ErrorState from '@/components/common/ErrorState.vue'
import KeyValueList from '@/components/common/KeyValueList.vue'
import KeyValueRow from '@/components/common/KeyValueRow.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import { keepOpenOnCompanion, keepOpenOnEscape, trackOverlay } from '@/components/common/overlayGuard'
import { Button } from '@/components/ui/button'
import { Sheet, SheetContent, SheetDescription, SheetTitle } from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { formatFullDateTime } from '@/i18n/format'
import { actionDetails, categoryOf, describeAction, paramText, resultText, sourceText } from './describe'

// One audit entry with everything recorded about it: who, when, from where,
// the result with its error code, and every parameter as stored. Opened
// from the table (`?open=entry:<id>`); links narrow the log to the same
// user or kind of action.
const props = defineProps<{
  open: boolean
  entry: AuditEntry | null
  loading?: boolean
  /** Why the entry could not be loaded. */
  error?: unknown
  providerNames: Readonly<Record<string, string>>
  /** Address of the log filtered by a user or category (the drawer closed). */
  filterLocation: (filter: { user?: string; category?: string }) => RouteLocationRaw
}>()

const emit = defineEmits<{ close: []; retry: [] }>()

const { t } = useI18n()
trackOverlay(() => props.open)

const params = computed(() =>
  Object.entries(props.entry?.params ?? {}).sort(([a], [b]) => a.localeCompare(b)),
)
const category = computed(() => (props.entry ? categoryOf(props.entry.action) : null))

function onOpenChange(value: boolean) {
  if (!value) emit('close')
}
</script>

<template>
  <Sheet :open="open" @update:open="onOpenChange">
    <SheetContent
      side="right"
      :show-close-button="false"
      class="h-dvh w-full max-w-none gap-0 p-0 sm:w-[520px] sm:max-w-[520px]"
      @interact-outside="keepOpenOnCompanion"
      @escape-key-down="keepOpenOnEscape"
    >
      <header class="flex items-start gap-3 border-b px-5 py-4">
        <div class="flex min-w-0 flex-1 flex-col gap-0.5">
          <SheetTitle class="text-base font-semibold [overflow-wrap:anywhere]">
            {{ entry ? describeAction(entry, providerNames) : t('audit.entry.title') }}
          </SheetTitle>
          <SheetDescription class="m-0 text-detail text-muted-foreground">
            <template v-if="entry">{{ formatFullDateTime(entry.ts) }}</template>
            <span v-else class="sr-only">{{ t('audit.entry.title') }}</span>
          </SheetDescription>
        </div>
        <Button variant="ghost" size="icon" class="-me-1.5 max-md:size-11" :aria-label="t('common.action.close')" @click="emit('close')">
          <X aria-hidden="true" />
        </Button>
      </header>

      <div class="min-h-0 flex-1 overflow-y-auto">
        <div class="flex flex-col gap-5 px-5 py-5">
          <ErrorState v-if="error" :error="error" compact @retry="emit('retry')" />
          <template v-else-if="loading || !entry">
            <Skeleton class="h-40 w-full rounded-lg" />
            <Skeleton class="h-24 w-full rounded-lg" />
          </template>
          <template v-else>
            <div v-if="actionDetails(entry).length > 0" class="flex flex-wrap gap-1.5">
              <StatusPill v-for="text in actionDetails(entry)" :key="text">{{ text }}</StatusPill>
            </div>

            <section class="overflow-hidden rounded-lg border bg-card">
              <KeyValueList>
                <KeyValueRow :label="t('audit.column.time')"><RelativeTime :value="entry.ts" /></KeyValueRow>
                <KeyValueRow :label="t('audit.column.user')">
                  <span v-if="entry.username" dir="ltr">{{ entry.username }}</span>
                  <span v-else class="text-muted-foreground">{{ t('audit.noUser') }}</span>
                </KeyValueRow>
                <KeyValueRow :label="t('audit.column.result')">
                  <StatusDot :tone="entry.result === 'ok' ? 'success' : 'destructive'">{{ resultText(entry) }}</StatusDot>
                </KeyValueRow>
                <KeyValueRow v-if="entry.result !== 'ok'" :label="t('audit.entry.errorCode')" mono>{{ entry.result }}</KeyValueRow>
                <KeyValueRow :label="t('audit.entry.actionCode')" mono>{{ entry.action }}</KeyValueRow>
                <KeyValueRow :label="t('audit.column.source')">{{ sourceText(entry.source) }}</KeyValueRow>
                <KeyValueRow :label="t('audit.column.remote')" mono>
                  <span dir="ltr">{{ entry.remote || '—' }}</span>
                </KeyValueRow>
                <KeyValueRow :label="t('audit.entry.id')" mono>{{ entry.id }}</KeyValueRow>
              </KeyValueList>
            </section>

            <section class="flex flex-col gap-2">
              <h3 class="m-0 text-sm font-semibold">{{ t('audit.entry.params') }}</h3>
              <p v-if="params.length === 0" class="m-0 text-detail text-muted-foreground">{{ t('audit.entry.noParams') }}</p>
              <div v-else class="overflow-hidden rounded-lg border bg-card">
                <dl class="m-0 divide-y">
                  <div v-for="[key, value] in params" :key="key" class="flex flex-col gap-0.5 px-4 py-2 sm:flex-row sm:gap-4">
                    <dt class="shrink-0 font-mono text-xs text-muted-foreground sm:w-36 sm:pt-px" dir="ltr">{{ key }}</dt>
                    <dd class="m-0 min-w-0 font-mono text-[12.5px] whitespace-pre-wrap [overflow-wrap:anywhere]" dir="ltr">{{ paramText(value) }}</dd>
                  </div>
                </dl>
              </div>
            </section>

            <nav class="flex flex-wrap gap-x-4 gap-y-2 text-detail max-md:gap-y-0" :aria-label="t('audit.entry.related')">
              <RouterLink
                v-if="entry.username"
                :to="filterLocation({ user: entry.username })"
                class="text-link underline-offset-2 hover:underline max-md:inline-flex max-md:min-h-11 max-md:items-center"
              >
                {{ t('audit.entry.byUser', { user: entry.username }) }}
              </RouterLink>
              <RouterLink
                v-if="category"
                :to="filterLocation({ category })"
                class="text-link underline-offset-2 hover:underline max-md:inline-flex max-md:min-h-11 max-md:items-center"
              >
                {{ t('audit.entry.byCategory', { category: t(`audit.category.${category}`) }) }}
              </RouterLink>
            </nav>
          </template>
        </div>
      </div>
    </SheetContent>
  </Sheet>
</template>
