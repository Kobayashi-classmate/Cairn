<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ClipboardPaste, CornerDownLeft, Pencil, Plus, Search, SquareTerminal, Trash2, X } from '@lucide/vue'
import { SNIPPETS_MAX, type TermSnippet } from '@/api/terminal'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import SkeletonRows from '@/components/common/SkeletonRows.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import { Button } from '@/components/ui/button'
import { confirmAction } from '@/composables/useConfirmDialog'
import { notifySuccess } from '@/lib/notify'
import { cn } from '@/lib/utils'
import { useTerminalStore } from '@/stores/terminal'
import { lineCount } from './labels'

// The account's saved commands, next to the terminal: Paste puts one into
// the terminal in front (a shell with bracketed paste waits for Enter even
// for several lines), Run pastes it and presses Enter.
const props = defineProps<{
  /** A terminal is open and connected. */
  canPaste: boolean
  /** Shows a Close button (the panel as a sheet, or collapsible). */
  closable?: boolean
}>()

const emit = defineEmits<{
  paste: [snippet: TermSnippet]
  run: [snippet: TermSnippet]
  add: []
  edit: [snippet: TermSnippet]
  close: []
}>()

const { t } = useI18n()
const store = useTerminalStore()
const filter = ref('')

onMounted(() => {
  if (!store.snippets) void store.load()
})

const list = computed(() => store.snippets ?? [])
const shown = computed(() => {
  const query = filter.value.trim().toLowerCase()
  return query ? list.value.filter((item) => `${item.name}\n${item.command}`.toLowerCase().includes(query)) : list.value
})
const full = computed(() => list.value.length >= SNIPPETS_MAX)

function askDelete(snippet: TermSnippet) {
  void confirmAction({
    title: t('terminal.snippets.deleteTitle', { name: snippet.name }),
    description: t('terminal.snippets.deleteText'),
    confirmLabel: t('terminal.snippets.delete'),
    destructive: true,
    run: async () => {
      await store.remove(snippet)
      notifySuccess(t('terminal.snippets.deleted', { name: snippet.name }))
    },
  })
}
</script>

<template>
  <section class="flex h-full min-h-0 flex-col" :aria-label="t('terminal.snippets.title')">
    <header class="flex items-center gap-2 border-b px-3 py-2.5">
      <h2 class="m-0 min-w-0 flex-1 truncate text-sm font-semibold">
        {{ t('terminal.snippets.title') }}
        <span v-if="store.snippets" class="ms-1 font-normal text-muted-foreground tabular-nums">{{ list.length }}</span>
      </h2>
      <Button
        variant="outline"
        size="sm"
        class="max-md:h-11"
        :disabled="full"
        :title="full ? t('errors.detailed.terminal.too_many_snippets', { max: SNIPPETS_MAX }) : undefined"
        @click="emit('add')"
      >
        <Plus aria-hidden="true" />
        {{ t('terminal.snippets.add') }}
      </Button>
      <Button
        v-if="closable"
        variant="ghost"
        size="icon-sm"
        class="max-md:size-11"
        :aria-label="t('terminal.snippets.hide')"
        :title="t('terminal.snippets.hide')"
        @click="emit('close')"
      >
        <X aria-hidden="true" />
      </Button>
    </header>

    <div v-if="list.length > 6" class="border-b px-3 py-2">
      <label
        class="flex h-8 items-center gap-2 rounded-md border border-input bg-card px-2 text-muted-foreground focus-within:border-ring focus-within:ring-2 focus-within:ring-ring/50 max-md:h-11"
      >
        <Search class="size-3.5 shrink-0" aria-hidden="true" />
        <input
          v-model="filter"
          type="search"
          :aria-label="t('terminal.snippets.filter')"
          :placeholder="t('terminal.snippets.filter')"
          class="h-full min-w-0 flex-1 bg-transparent text-base text-foreground outline-none placeholder:text-muted-foreground md:text-detail"
        />
      </label>
    </div>

    <div class="min-h-0 flex-1 overflow-y-auto">
      <ErrorState v-if="store.error && !store.snippets" :error="store.error" compact @retry="store.load()" />
      <SkeletonRows v-else-if="!store.snippets" :rows="3" two-line />
      <EmptyState
        v-else-if="list.length === 0"
        :icon="SquareTerminal"
        compact
        :title="t('terminal.snippets.empty')"
        :text="t('terminal.snippets.emptyText')"
      >
        <template #actions>
          <Button variant="outline" size="sm" @click="emit('add')">
            <Plus aria-hidden="true" />
            {{ t('terminal.snippets.add') }}
          </Button>
        </template>
      </EmptyState>
      <p v-else-if="shown.length === 0" class="m-0 px-3 py-4 text-detail text-muted-foreground">
        {{ t('common.table.noMatches', { query: filter.trim() }) }}
      </p>
      <ul v-else class="m-0 flex list-none flex-col gap-2 p-3">
        <li v-for="snippet in shown" :key="snippet.id" class="flex flex-col gap-2 rounded-md border bg-card p-2.5">
          <div class="flex min-w-0 items-center gap-2">
            <span class="min-w-0 flex-1 truncate text-detail font-semibold" dir="auto" :title="snippet.name">{{ snippet.name }}</span>
            <StatusPill v-if="lineCount(snippet.command) > 1" tone="neutral">
              {{ t('terminal.snippets.lines', { count: lineCount(snippet.command) }, lineCount(snippet.command)) }}
            </StatusPill>
          </div>
          <pre
            class="m-0 line-clamp-3 rounded-sm bg-muted px-2 py-1.5 font-mono text-xs leading-relaxed whitespace-pre-wrap text-muted-foreground [overflow-wrap:anywhere]"
            dir="ltr"
            :title="snippet.command"
          >{{ snippet.command }}</pre>
          <div class="flex flex-wrap items-center gap-1">
            <Button
              variant="outline"
              size="sm"
              class="max-md:h-11"
              :disabled="!props.canPaste"
              :title="props.canPaste ? t('terminal.snippets.pasteHint') : t('terminal.snippets.noTerminal')"
              @click="emit('paste', snippet)"
            >
              <ClipboardPaste aria-hidden="true" />
              {{ t('terminal.snippets.paste') }}
            </Button>
            <Button
              variant="outline"
              size="sm"
              class="max-md:h-11"
              :disabled="!props.canPaste"
              :title="props.canPaste ? t('terminal.snippets.runHint') : t('terminal.snippets.noTerminal')"
              @click="emit('run', snippet)"
            >
              <CornerDownLeft aria-hidden="true" />
              {{ t('terminal.snippets.run') }}
            </Button>
            <span class="flex-1" />
            <Button
              variant="ghost"
              size="icon-sm"
              class="max-md:size-11"
              :aria-label="t('terminal.snippets.editNamed', { name: snippet.name })"
              :title="t('common.action.edit')"
              @click="emit('edit', snippet)"
            >
              <Pencil aria-hidden="true" />
            </Button>
            <Button
              variant="ghost"
              size="icon-sm"
              :class="cn('text-destructive hover:bg-destructive-soft hover:text-destructive max-md:size-11')"
              :aria-label="t('terminal.snippets.deleteNamed', { name: snippet.name })"
              :title="t('common.action.delete')"
              @click="askDelete(snippet)"
            >
              <Trash2 aria-hidden="true" />
            </Button>
          </div>
        </li>
      </ul>
    </div>
  </section>
</template>
