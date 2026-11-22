<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMediaQuery } from '@vueuse/core'
import { CheckCheck, LoaderCircle, MailOpen, Megaphone, X } from '@lucide/vue'
import type { Announcement } from '@/api/announcements'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import PageHeader from '@/components/common/PageHeader.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import SkeletonRows from '@/components/common/SkeletonRows.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import { keepOpenOnCompanion, keepOpenOnEscape, trackOverlay } from '@/components/common/overlayGuard'
import { Button } from '@/components/ui/button'
import { Sheet, SheetContent, SheetDescription, SheetTitle } from '@/components/ui/sheet'
import { useDrawerRoute } from '@/composables/useDrawerRoute'
import { pickLocalized } from '@/i18n'
import { cn } from '@/lib/utils'
import { notifyError } from '@/lib/notify'
import { useAnnouncementsStore } from '@/stores/announcements'
import { levelText, levelTone } from '@/views/notify/labels'
import MessageReader from './messages/MessageReader.vue'

// Official messages from the Cairn project: the list (unread first marked
// with a dot) and a reading pane beside it from 1024px; narrower, a message
// opens in a sheet. The open message is kept in the address
// (`?open=message:<id>`). Reading does not mark a message as read; the
// button does.
const { t } = useI18n()
const store = useAnnouncementsStore()
const drawer = useDrawerRoute('message')
const wide = useMediaQuery('(min-width: 1024px)')
const markingAll = ref(false)

onMounted(() => void store.refresh())

const items = computed(() => store.items)
const opened = computed<Announcement | null>(() => items.value.find((item) => item.id === drawer.id.value) ?? null)
/** The pane shows the opened message, or the newest one. */
const shown = computed<Announcement | null>(() => opened.value ?? (wide.value ? (items.value[0] ?? null) : null))

const sheetOpen = computed(() => !wide.value && opened.value !== null)
trackOverlay(() => sheetOpen.value)

// A message named in the address that is not (or no longer) in the list.
watch(
  () => [store.loaded, drawer.id.value] as const,
  ([loaded, id]) => {
    if (loaded && id !== null && !opened.value) drawer.close()
  },
)

const status = computed(() => {
  if (!store.loaded) return ''
  const total = items.value.length
  const parts = [t('messages.status.count', { count: total }, total)]
  if (store.unread > 0) parts.push(t('messages.status.unread', { count: store.unread }, store.unread))
  return parts.join(' · ')
})

function open(item: Announcement) {
  drawer.open(item.id)
}

function onSheetOpenChange(value: boolean) {
  if (!value) drawer.close()
}

async function markAll() {
  markingAll.value = true
  try {
    await store.markAllRead()
  } catch (err) {
    notifyError(err)
  } finally {
    markingAll.value = false
  }
}
</script>

<template>
  <div>
    <PageHeader :title="t('messages.title')" :status="status" :status-loading="!store.loaded && !store.error">
      <template #actions>
        <Button variant="outline" class="max-md:h-11" :disabled="store.unread === 0 || markingAll" @click="markAll">
          <LoaderCircle v-if="markingAll" class="animate-spin" aria-hidden="true" />
          <CheckCheck v-else aria-hidden="true" />
          {{ t('messages.markAllRead') }}
        </Button>
      </template>
    </PageHeader>

    <div class="grid items-start gap-5 lg:grid-cols-[minmax(0,380px)_minmax(0,1fr)]">
      <section :aria-label="t('messages.listLabel')" class="min-w-0 overflow-hidden rounded-lg border bg-card">
        <p class="m-0 border-b px-4 py-2.5 text-detail text-muted-foreground md:px-[18px]">{{ t('messages.intro') }}</p>
        <ErrorState v-if="store.error && items.length === 0" :error="store.error" compact @retry="store.refresh()" />
        <SkeletonRows v-else-if="!store.loaded" :rows="4" two-line />
        <EmptyState v-else-if="items.length === 0" :icon="Megaphone" :title="t('messages.empty')" :text="t('messages.emptyText')" compact />
        <ul v-else class="m-0 list-none divide-y p-0">
          <li v-for="item in items" :key="item.id">
            <button
              type="button"
              :aria-current="shown?.id === item.id ? 'true' : undefined"
              :class="
                cn(
                  'flex w-full items-start gap-3 px-4 py-3 text-start outline-none transition-colors hover:bg-accent/50 focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:ring-inset md:px-[18px]',
                  shown?.id === item.id && 'bg-primary-soft/60 hover:bg-primary-soft/60',
                )
              "
              @click="open(item)"
            >
              <span
                :class="cn('mt-[7px] size-2 shrink-0 rounded-full', item.read ? 'bg-transparent' : 'bg-primary')"
                aria-hidden="true"
              />
              <span class="flex min-w-0 flex-1 flex-col gap-1">
                <span :class="cn('line-clamp-2 [overflow-wrap:anywhere]', item.read ? 'font-medium' : 'font-semibold')">
                  <span v-if="!item.read" class="sr-only">{{ t('messages.unread') }}:</span>
                  {{ pickLocalized(item.title) }}
                </span>
                <span class="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
                  <StatusPill :tone="levelTone(item.level)">{{ levelText(item.level) }}</StatusPill>
                  <RelativeTime :value="item.publishedAt" />
                </span>
              </span>
            </button>
          </li>
        </ul>
      </section>

      <!-- Reading pane (wide screens). -->
      <section v-if="wide" :aria-label="t('messages.readerLabel')" class="min-w-0 rounded-lg border bg-card px-5 py-5 lg:px-6">
        <MessageReader v-if="shown" :key="shown.id" :message="shown" />
        <EmptyState v-else-if="store.loaded" :icon="MailOpen" :title="t('messages.nothingOpen')" compact />
        <SkeletonRows v-else :rows="3" />
      </section>
    </div>

    <!-- Narrow screens: the message opens in a sheet. -->
    <Sheet :open="sheetOpen" @update:open="onSheetOpenChange">
      <SheetContent
        side="right"
        :show-close-button="false"
        class="h-dvh w-full max-w-none gap-0 p-0 sm:w-[560px] sm:max-w-[560px]"
        @interact-outside="keepOpenOnCompanion"
        @escape-key-down="keepOpenOnEscape"
      >
        <header class="flex items-center justify-between gap-3 border-b px-5 py-3">
          <SheetTitle class="text-base font-semibold">{{ t('messages.title') }}</SheetTitle>
          <SheetDescription class="sr-only">{{ opened ? pickLocalized(opened.title) : '' }}</SheetDescription>
          <Button variant="ghost" size="icon" class="-me-1.5 max-md:size-11" :aria-label="t('common.action.close')" @click="drawer.close()">
            <X aria-hidden="true" />
          </Button>
        </header>
        <div class="min-h-0 flex-1 overflow-y-auto px-5 py-5">
          <MessageReader v-if="opened" :key="opened.id" :message="opened" />
        </div>
      </SheetContent>
    </Sheet>
  </div>
</template>
