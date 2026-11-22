<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { File, Folder, LoaderCircle, Lock, Search, SearchX, X } from '@lucide/vue'
import { isAbortError, isStepUpRequired } from '@/api/client'
import { searchFiles, type FileMatch } from '@/api/files'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import Notice from '@/components/common/Notice.vue'
import { keepOpenOnCompanion, keepOpenOnEscape, trackOverlay } from '@/components/common/overlayGuard'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Sheet, SheetClose, SheetContent, SheetDescription, SheetTitle } from '@/components/ui/sheet'
import { absolutePath } from '@/files/paths'
import { byteLength } from '@/forms/rules'
import { validationMessage } from '@/i18n/errors'

// Finds entries below the folder shown, by name or by the text inside
// files. Picking a folder goes there; picking a file opens it (at the line
// for text matches).
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{ root: string; dir: string }>()
const emit = defineEmits<{ pick: [match: FileMatch] }>()

/** Longest search text the server takes, in UTF-8 bytes. */
const QUERY_MAX = 256

const { t } = useI18n()
trackOverlay(() => open.value)
const query = ref('')
const content = ref(false)
const caseSensitive = ref(false)
const matches = ref<FileMatch[]>([])
const truncated = ref(false)
const searched = ref(false)
const loading = ref(false)
const error = ref<unknown>(null)
const input = ref<InstanceType<typeof Input> | null>(null)
let controller: AbortController | null = null
/** The server counts bytes: 256 characters of Chinese are far too many. */
const queryError = computed(() => (byteLength(query.value) > QUERY_MAX ? validationMessage('query', 'max') : null))

// A new place to search in starts afresh.
watch(
  () => [props.root, props.dir] as const,
  () => {
    controller?.abort()
    matches.value = []
    searched.value = false
    truncated.value = false
    error.value = null
  },
)

function onOpenAutoFocus(event: Event) {
  event.preventDefault()
  void nextTick(() => (input.value?.$el as HTMLInputElement | undefined)?.focus())
}

async function search() {
  const q = query.value
  if (!q.trim() || queryError.value) return
  controller?.abort()
  const request = new AbortController()
  controller = request
  loading.value = true
  error.value = null
  try {
    const result = await searchFiles(props.root, props.dir, q, { content: content.value, caseSensitive: caseSensitive.value }, request.signal)
    matches.value = result.matches ?? []
    truncated.value = !!result.truncated
    searched.value = true
  } catch (err) {
    if (!isAbortError(err)) error.value = err
  } finally {
    if (controller === request) {
      controller = null
      loading.value = false
    }
  }
}

function pick(match: FileMatch) {
  open.value = false
  emit('pick', match)
}
</script>

<template>
  <Sheet v-model:open="open">
    <SheetContent
      side="right"
      :show-close-button="false"
      class="h-dvh w-full max-w-none gap-0 p-0 sm:w-[520px] sm:max-w-[520px]"
      @open-auto-focus="onOpenAutoFocus"
      @interact-outside="keepOpenOnCompanion"
      @escape-key-down="keepOpenOnEscape"
    >
      <header class="flex items-start gap-3 border-b px-5 py-4">
        <div class="flex min-w-0 flex-1 flex-col gap-0.5">
          <SheetTitle class="text-base font-semibold">{{ t('files.search.title') }}</SheetTitle>
          <SheetDescription class="font-mono text-xs text-muted-foreground [overflow-wrap:anywhere]" dir="ltr">
            {{ t('files.search.where', { dir: absolutePath(root, dir) }) }}
          </SheetDescription>
        </div>
        <SheetClose as-child>
          <Button variant="ghost" size="icon" class="-me-1.5 max-md:size-11" :aria-label="t('common.action.close')">
            <X aria-hidden="true" />
          </Button>
        </SheetClose>
      </header>

      <form class="flex flex-col gap-3 border-b px-5 py-4" novalidate role="search" @submit.prevent="search">
        <div class="flex gap-2">
          <Input
            ref="input"
            v-model="query"
            type="search"
            :maxlength="QUERY_MAX"
            :placeholder="t('files.search.placeholder')"
            :aria-label="t('files.search.placeholder')"
            :aria-invalid="queryError ? true : undefined"
            :aria-describedby="queryError ? 'files-search-error' : undefined"
            autocomplete="off"
            autocapitalize="none"
            spellcheck="false"
            class="max-md:h-11"
          />
          <Button type="submit" class="max-md:h-11" :disabled="!query.trim() || !!queryError || loading">
            <LoaderCircle v-if="loading" class="animate-spin" aria-hidden="true" />
            <Search v-else aria-hidden="true" />
            {{ t('files.search.submit') }}
          </Button>
        </div>
        <p v-if="queryError" id="files-search-error" class="m-0 text-xs font-medium text-destructive">{{ queryError }}</p>
        <div class="flex flex-wrap gap-x-5 gap-y-2">
          <label class="flex cursor-pointer items-center gap-2 text-detail">
            <Checkbox :model-value="content" @update:model-value="(value: boolean | 'indeterminate') => (content = value === true)" />
            {{ t('files.search.content') }}
          </label>
          <label class="flex cursor-pointer items-center gap-2 text-detail">
            <Checkbox :model-value="caseSensitive" @update:model-value="(value: boolean | 'indeterminate') => (caseSensitive = value === true)" />
            {{ t('files.search.case') }}
          </label>
        </div>
        <p v-if="content" class="m-0 text-xs text-muted-foreground">{{ t('files.search.contentHint') }}</p>
      </form>

      <div class="min-h-0 flex-1 overflow-y-auto" :aria-busy="loading || undefined">
        <EmptyState v-if="isStepUpRequired(error)" :icon="Lock" compact :title="t('files.stepUp.search')" :text="t('files.stepUp.text')">
          <template #actions>
            <Button variant="outline" size="sm" class="max-md:h-11" @click="search">{{ t('common.action.retry') }}</Button>
          </template>
        </EmptyState>
        <ErrorState v-else-if="error" :error="error" compact @retry="search" />
        <EmptyState v-else-if="searched && matches.length === 0" :icon="SearchX" compact :title="t('files.search.none')" />
        <template v-else-if="matches.length > 0">
          <div v-if="truncated" class="px-5 pt-4">
            <Notice tone="info">{{ t('files.search.truncated') }}</Notice>
          </div>
          <p class="m-0 px-5 pt-3 pb-1 text-caption text-muted-foreground" role="status">
            {{ t('files.search.count', { count: matches.length }, matches.length) }}
          </p>
          <ul class="m-0 list-none px-3 pb-4">
            <li v-for="(match, index) in matches" :key="`${match.path}:${match.line ?? 0}:${index}`">
              <button
                type="button"
                class="flex w-full gap-2.5 rounded-md px-2 py-2 text-start outline-none hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring/50"
                @click="pick(match)"
              >
                <Folder v-if="match.type === 'dir'" class="mt-0.5 size-4 shrink-0 text-primary" aria-hidden="true" />
                <File v-else class="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
                <span class="flex min-w-0 flex-col gap-0.5">
                  <span class="font-mono text-[13px] [overflow-wrap:anywhere]" dir="ltr">
                    {{ match.path }}<span v-if="match.line" class="text-muted-foreground">:{{ match.line }}</span>
                  </span>
                  <span v-if="match.text" class="font-mono text-xs whitespace-pre-wrap text-muted-foreground [overflow-wrap:anywhere]" dir="ltr">{{ match.text }}</span>
                </span>
              </button>
            </li>
          </ul>
        </template>
        <p v-else class="m-0 px-5 py-6 text-detail text-muted-foreground">{{ t('files.search.intro') }}</p>
      </div>
    </SheetContent>
  </Sheet>
</template>
