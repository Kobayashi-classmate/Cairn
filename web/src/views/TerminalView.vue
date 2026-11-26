<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMediaQuery } from '@vueuse/core'
import { BookMarked, Plus, RotateCcw, SquareTerminal, X } from '@lucide/vue'
import { isApiError } from '@/api/client'
import {
  TERMINALS_MAX,
  closeTermSession,
  fetchTermSessions,
  fetchTermUsers,
  openTermSession,
  setRootTerminal,
  type TermConnection,
  type TermSession,
  type TermSnippet,
  type TermUsers,
} from '@/api/terminal'
import AccessSwitch from '@/components/common/AccessSwitch.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import Notice from '@/components/common/Notice.vue'
import PageHeader from '@/components/common/PageHeader.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import { keepOpenOnCompanion, keepOpenOnEscape, trackOverlay } from '@/components/common/overlayGuard'
import { Button } from '@/components/ui/button'
import { Sheet, SheetContent, SheetDescription, SheetTitle } from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { confirmAction } from '@/composables/useConfirmDialog'
import { notifyError, notifySuccess } from '@/lib/notify'
import { TONE_DOT, type Tone } from '@/lib/tone'
import { cn } from '@/lib/utils'
import { useSessionStore } from '@/stores/session'
import { useTerminalStore } from '@/stores/terminal'
import { lineCount, userLabel } from './terminal/labels'
import NewTerminalSheet from './terminal/NewTerminalSheet.vue'
import SnippetSheet from './terminal/SnippetSheet.vue'
import SchemeMenu from './terminal/SchemeMenu.vue'
import SnippetsPanel from './terminal/SnippetsPanel.vue'

// Web terminal: one tab per session, the terminal filling the rest of the
// window, and the account's saved commands beside it (a sheet on narrow
// screens). Sessions outlive the page for a few minutes, so a reload lists
// them and joins them again.
const TerminalPane = defineAsyncComponent(() => import('./terminal/TerminalPane.vue'))

/** Remembers whether the saved commands are shown beside the terminal. */
const PANEL_KEY = 'cairn.terminal.snippets'
/** Size a terminal opened again starts with; it reports its real size once shown. */
const START_COLS = 120
const START_ROWS = 32
/** Shortest the terminal area gets (px). */
const MIN_HEIGHT = 360

type Pane = { paste: (text: string) => void; run: (text: string) => void; bracketedPaste: () => boolean; focus: () => void }

const { t } = useI18n()
const session = useSessionStore()
const snippets = useTerminalStore()
const wide = useMediaQuery('(min-width: 1024px)')

const canAdmin = computed(() => session.can('terminal.admin'))

const users = ref<TermUsers | null>(null)
const sessions = ref<TermSession[]>([])
const states = ref<Record<string, TermConnection>>({})
const active = ref<string | undefined>(undefined)
const loading = ref(true)
const loadError = ref<unknown>(null)
const showNew = ref(false)
const newUser = ref<string | undefined>(undefined)
const switching = ref(false)
const reopening = ref<string | null>(null)
const panes = ref<Record<string, Pane | null>>({})

const panelOpen = ref(readPanel())
const panelSheet = ref(false)
trackOverlay(() => panelSheet.value && !wide.value)
const showSnippet = ref(false)
const editing = ref<TermSnippet | null>(null)

function readPanel(): boolean {
  try {
    return window.localStorage.getItem(PANEL_KEY) !== '0'
  } catch {
    return true
  }
}

// Growing past the narrow layout, the saved commands move from the sheet to the side.
watch(wide, (value) => {
  if (value) panelSheet.value = false
})

watch(panelOpen, (value) => {
  try {
    window.localStorage.setItem(PANEL_KEY, value ? '1' : '0')
  } catch {
    // Not remembered; the panel still opens and closes for this visit.
  }
})

async function loadUsers() {
  users.value = await fetchTermUsers()
}

async function load() {
  loading.value = true
  try {
    const [, list] = await Promise.all([loadUsers(), fetchTermSessions()])
    sessions.value = list
    loadError.value = null
    if (!active.value || !list.some((entry) => entry.id === active.value)) active.value = list[list.length - 1]?.id
  } catch (err) {
    loadError.value = err
  } finally {
    loading.value = false
  }
}

onMounted(load)

const full = computed(() => sessions.value.length >= TERMINALS_MAX)
const current = computed(() => sessions.value.find((entry) => entry.id === active.value) ?? null)
const currentState = computed(() => (current.value ? (states.value[current.value.id] ?? 'connecting') : null))
const canPaste = computed(() => currentState.value === 'open')

const status = computed(() => {
  if (loading.value && !users.value) return ''
  const count = sessions.value.length
  return count === 0 ? t('terminal.status.none') : t('terminal.status.count', { count, max: TERMINALS_MAX }, count)
})

function siteOf(user: string): string | undefined {
  return users.value?.users.find((entry) => entry.name === user)?.site
}

function label(entry: TermSession): string {
  return userLabel(entry.user, siteOf(entry.user))
}

const STATE_TONES: Record<TermConnection, Tone> = {
  connecting: 'progress',
  open: 'success',
  reconnecting: 'warning',
  ended: 'neutral',
}

function stateOf(id: string): TermConnection {
  return states.value[id] ?? 'connecting'
}

function setPane(id: string, pane: unknown) {
  panes.value[id] = (pane as Pane | null) ?? null
}

/**
 * A terminal changed state. One that ended may have been closed by the
 * server because the account changed (its role, say): who it may open a
 * terminal as is read again before offering a new one.
 */
function onState(id: string, state: TermConnection) {
  const before = states.value[id]
  states.value[id] = state
  if (state === 'ended' && before !== 'ended') void loadUsers().catch(() => undefined)
}

/** A new terminal can be opened as the same user as an ended one. */
function canReopen(entry: TermSession): boolean {
  const user = users.value?.users.find((item) => item.name === entry.user)
  return !!user && (user.kind !== 'root' || users.value?.rootTerminal === true)
}

// Opening and closing ---------------------------------------------------------------

function openNew(user?: string) {
  newUser.value = user
  showNew.value = true
}

function opened(created: TermSession) {
  sessions.value.push(created)
  active.value = created.id
}

function forget(id: string) {
  const index = sessions.value.findIndex((item) => item.id === id)
  if (index < 0) return
  sessions.value.splice(index, 1)
  delete states.value[id]
  delete panes.value[id]
  if (active.value === id) active.value = sessions.value[Math.min(index, sessions.value.length - 1)]?.id
}

async function closeOnServer(id: string) {
  try {
    await closeTermSession(id)
  } catch (err) {
    // A session that already ended is gone on the server too.
    if (!(isApiError(err) && err.status === 404)) throw err
  }
}

async function close(entry: TermSession) {
  if (stateOf(entry.id) === 'ended') {
    try {
      await closeOnServer(entry.id)
    } catch (err) {
      notifyError(err)
      return
    }
    forget(entry.id)
    return
  }
  const ok = await confirmAction({
    title: t('terminal.close.title', { name: label(entry) }),
    description: t('terminal.close.text'),
    confirmLabel: t('terminal.close.submit'),
    destructive: true,
    run: () => closeOnServer(entry.id),
  })
  if (ok) forget(entry.id)
}

/** Opens a terminal as the same user as an ended one, in its place. */
async function reopen(entry: TermSession) {
  reopening.value = entry.id
  try {
    // Step-up protected: the client asks for the password and retries.
    const created = await openTermSession(entry.user, START_COLS, START_ROWS)
    const index = sessions.value.findIndex((item) => item.id === entry.id)
    void closeOnServer(entry.id).catch(() => undefined)
    delete states.value[entry.id]
    delete panes.value[entry.id]
    if (index >= 0) sessions.value.splice(index, 1, created)
    else sessions.value.push(created)
    active.value = created.id
  } catch (err) {
    notifyError(err)
  } finally {
    reopening.value = null
  }
}

/** Arrow keys move between the terminal tabs. */
function onTabKey(event: KeyboardEvent, index: number) {
  const step = event.key === 'ArrowRight' ? 1 : event.key === 'ArrowLeft' ? -1 : 0
  if (step === 0 || sessions.value.length === 0) return
  event.preventDefault()
  const next = sessions.value[(index + step + sessions.value.length) % sessions.value.length]
  if (!next) return
  active.value = next.id
  void nextTick(() => document.getElementById(`term-tab-${next.id}`)?.focus())
}

// Root terminals ----------------------------------------------------------------------

async function toggleRoot(on: boolean) {
  if (on) {
    const ok = await confirmAction({
      title: t('terminal.root.enableTitle'),
      description: t('terminal.root.enableText'),
      confirmLabel: t('terminal.root.enable'),
      destructive: true,
    })
    if (!ok) return
  }
  switching.value = true
  try {
    await setRootTerminal(on)
    await loadUsers()
    notifySuccess(on ? t('terminal.root.enabled') : t('terminal.root.disabled'))
  } catch (err) {
    notifyError(err)
  } finally {
    switching.value = false
  }
}

// Saved commands -----------------------------------------------------------------------

function togglePanel() {
  if (wide.value) panelOpen.value = !panelOpen.value
  else panelSheet.value = !panelSheet.value
}

const panelShown = computed(() => (wide.value ? panelOpen.value : panelSheet.value))

function addSnippet() {
  editing.value = null
  showSnippet.value = true
}

function editSnippet(snippet: TermSnippet) {
  editing.value = snippet
  showSnippet.value = true
}

async function useSnippet(snippet: TermSnippet, run: boolean) {
  const pane = current.value ? panes.value[current.value.id] : null
  if (!pane || !canPaste.value) return
  // A trailing line end would press Enter (twice with Run).
  const text = snippet.command.replace(/\n+$/, '')
  // Without bracketed paste a shell runs each pasted line on its own.
  if (!run && lineCount(text) > 1 && !pane.bracketedPaste()) {
    const ok = await confirmAction({
      title: t('terminal.snippets.unbracketedTitle'),
      description: t('terminal.snippets.unbracketedText'),
      confirmLabel: t('terminal.snippets.pasteAnyway'),
    })
    if (!ok) return
  }
  panelSheet.value = false
  if (run) pane.run(text)
  else pane.paste(text)
  // The confirmation hands the focus back to its button; the terminal wants it.
  setTimeout(() => pane.focus(), 0)
}

// Height ---------------------------------------------------------------------------------

const workspace = ref<HTMLElement | null>(null)
const height = ref(MIN_HEIGHT)

/** The terminal area fills the window below the page header. */
function measure() {
  const el = workspace.value
  if (!el) return
  const top = el.getBoundingClientRect().top + window.scrollY
  const main = document.getElementById('main')
  const bottom = main ? Number.parseFloat(getComputedStyle(main).paddingBottom) || 0 : 0
  height.value = Math.max(MIN_HEIGHT, Math.floor(window.innerHeight - top - bottom))
}

const head = ref<HTMLElement | null>(null)
let headerObserver: ResizeObserver | null = null

onMounted(() => {
  window.addEventListener('resize', measure)
  // The header wraps onto more lines on narrow screens.
  headerObserver = new ResizeObserver(() => measure())
  if (head.value) headerObserver.observe(head.value)
  void nextTick(measure)
})

onBeforeUnmount(() => {
  window.removeEventListener('resize', measure)
  headerObserver?.disconnect()
  // Saved commands belong to the account; read them again next time.
  snippets.clear()
})

watch(workspace, () => void nextTick(measure))
</script>

<template>
  <div>
    <div ref="head">
      <PageHeader :title="t('terminal.title')" :status="status" :status-loading="loading && !users">
        <template #actions>
          <SchemeMenu />
          <AccessSwitch
            v-if="canAdmin && users"
            :label="t('terminal.root.label')"
            :description="t('terminal.root.enableText')"
            :on="users.rootTerminal"
            :locked="users.locked"
            :locked-text="t('terminal.root.locked')"
            :busy="switching"
            @update="toggleRoot"
          />
          <Button class="max-md:h-11" :disabled="!users || full" :title="full ? t('errors.detailed.term.too_many', { max: TERMINALS_MAX }) : undefined" @click="openNew()">
            <Plus aria-hidden="true" />
            {{ t('terminal.new.button') }}
          </Button>
        </template>
      </PageHeader>
    </div>

    <ErrorState v-if="loadError" :error="loadError" class="rounded-lg border bg-card" @retry="load" />
    <div v-else-if="loading && !users" class="flex flex-col gap-3 rounded-lg border bg-card p-4" :style="{ height: `${height}px` }" aria-hidden="true">
      <div class="flex gap-2">
        <Skeleton class="h-8 w-36" />
        <Skeleton class="h-8 w-28" />
      </div>
      <Skeleton class="w-full flex-1" />
    </div>
    <section
      v-else
      ref="workspace"
      class="flex min-w-0 flex-col overflow-hidden rounded-lg border bg-card"
      :style="{ height: `${height}px` }"
      :aria-label="t('terminal.title')"
    >
      <div class="flex min-w-0 items-stretch border-b bg-muted/50">
        <div class="relative flex min-w-0 flex-1 items-stretch overflow-x-auto [scrollbar-width:thin]">
          <!-- A tab list may only contain tabs: it owns the tab buttons, and each
               tab's close button stays next to it in the strip, outside the list. -->
          <div
            role="tablist"
            :aria-label="t('terminal.tabs')"
            :aria-owns="sessions.map((entry) => `term-tab-${entry.id}`).join(' ') || undefined"
          />
          <div
            v-for="(entry, index) in sessions"
            :key="entry.id"
            :class="
              cn(
                'group relative flex shrink-0 items-center border-e',
                entry.id === active ? 'bg-card text-foreground' : 'text-muted-foreground hover:bg-accent/60 hover:text-foreground',
              )
            "
          >
            <span v-if="entry.id === active" class="absolute inset-x-0 bottom-0 h-0.5 bg-primary" aria-hidden="true" />
            <button
              :id="`term-tab-${entry.id}`"
              type="button"
              role="tab"
              :aria-selected="entry.id === active"
              :aria-controls="`term-panel-${entry.id}`"
              :tabindex="entry.id === active ? 0 : -1"
              class="flex h-10 max-w-60 items-center gap-2 ps-3 pe-1.5 text-detail font-medium outline-none focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:ring-inset max-md:h-11"
              :title="`${label(entry)} · ${t(`terminal.state.${stateOf(entry.id)}`)}`"
              @click="active = entry.id"
              @keydown="onTabKey($event, index)"
            >
              <span
                :class="cn('size-2 shrink-0 rounded-full', TONE_DOT[STATE_TONES[stateOf(entry.id)]], stateOf(entry.id) === 'connecting' && 'animate-pulse')"
                aria-hidden="true"
              />
              <span class="truncate" dir="ltr">{{ label(entry) }}</span>
              <span class="sr-only">{{ t(`terminal.state.${stateOf(entry.id)}`) }}</span>
            </button>
            <button
              type="button"
              class="me-1.5 inline-flex size-6 shrink-0 items-center justify-center rounded-sm text-muted-foreground outline-none hover:bg-accent hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 max-md:me-0 max-md:size-11"
              :aria-label="t('terminal.close.named', { name: label(entry) })"
              :title="t('terminal.close.submit')"
              @click="close(entry)"
            >
              <X class="size-3.5" aria-hidden="true" />
            </button>
          </div>
          <button
            v-if="!full && sessions.length > 0"
            type="button"
            class="inline-flex w-10 shrink-0 items-center justify-center text-muted-foreground outline-none hover:bg-accent/60 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:ring-inset max-md:w-11"
            :aria-label="t('terminal.new.button')"
            :title="t('terminal.new.button')"
            :disabled="!users"
            @click="openNew()"
          >
            <Plus class="size-4" aria-hidden="true" />
          </button>
        </div>
        <button
          type="button"
          :aria-pressed="panelShown"
          :class="
            cn(
              'inline-flex shrink-0 items-center gap-2 border-s px-3 text-detail font-medium outline-none focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:ring-inset max-md:px-3.5',
              panelShown ? 'bg-card text-foreground' : 'text-muted-foreground hover:bg-accent/60 hover:text-foreground',
            )
          "
          :title="t('terminal.snippets.title')"
          @click="togglePanel"
        >
          <BookMarked class="size-4" aria-hidden="true" />
          <span class="max-sm:sr-only">{{ t('terminal.snippets.title') }}</span>
        </button>
      </div>

      <div class="flex min-h-0 flex-1">
        <div class="flex min-h-0 min-w-0 flex-1 flex-col">
          <template v-if="current">
            <div class="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 border-b px-3 py-1.5 text-xs text-muted-foreground">
              <span class="font-mono text-foreground" dir="ltr">{{ current.user }}</span>
              <span aria-hidden="true">·</span>
              <span class="font-mono" dir="ltr">{{ current.shell }}</span>
              <span aria-hidden="true">·</span>
              <span class="min-w-0 truncate font-mono" dir="ltr">{{ current.home }}</span>
              <span aria-hidden="true">·</span>
              <i18n-t keypath="terminal.opened" tag="span">
                <template #time><RelativeTime :value="current.createdAt" /></template>
              </i18n-t>
              <span class="ms-auto max-lg:hidden">{{ t('terminal.hint') }}</span>
              <span class="max-lg:hidden">{{ t('terminal.focusHint') }}</span>
            </div>
            <div v-if="currentState === 'ended'" class="border-b px-3 py-2">
              <Notice tone="info">
                {{ t('terminal.endedNotice') }}
                <template #action>
                  <Button
                    v-if="canReopen(current)"
                    variant="outline"
                    size="sm"
                    class="max-md:h-11"
                    :disabled="reopening === current.id || full"
                    @click="reopen(current)"
                  >
                    <RotateCcw aria-hidden="true" />
                    {{ t('terminal.reopen', { user: current.user }) }}
                  </Button>
                  <Button variant="ghost" size="sm" class="max-md:h-11" @click="close(current)">{{ t('terminal.closeTab') }}</Button>
                </template>
              </Notice>
            </div>
          </template>

          <div
            v-for="entry in sessions"
            v-show="entry.id === active"
            :id="`term-panel-${entry.id}`"
            :key="entry.id"
            role="tabpanel"
            :aria-labelledby="`term-tab-${entry.id}`"
            class="min-h-0 flex-1"
          >
            <TerminalPane
              :ref="(pane: unknown) => setPane(entry.id, pane)"
              :session-id="entry.id"
              :active="active === entry.id"
              @status="(state: TermConnection) => onState(entry.id, state)"
            />
          </div>

          <EmptyState
            v-if="sessions.length === 0"
            :icon="SquareTerminal"
            class="my-auto"
            :title="t('terminal.empty.title')"
            :text="canAdmin ? t('terminal.empty.text') : t('terminal.empty.textSites')"
          >
            <template #actions>
              <Button :disabled="!users" @click="openNew()">
                <Plus aria-hidden="true" />
                {{ t('terminal.new.button') }}
              </Button>
            </template>
          </EmptyState>
        </div>

        <aside v-if="wide && panelOpen" class="flex w-[300px] shrink-0 flex-col border-s bg-background/40">
          <SnippetsPanel
            :can-paste="canPaste"
            closable
            @paste="(snippet: TermSnippet) => useSnippet(snippet, false)"
            @run="(snippet: TermSnippet) => useSnippet(snippet, true)"
            @add="addSnippet"
            @edit="editSnippet"
            @close="panelOpen = false"
          />
        </aside>
      </div>
    </section>

    <Sheet :open="panelSheet && !wide" @update:open="(value: boolean) => (panelSheet = value)">
      <SheetContent
        side="right"
        :show-close-button="false"
        class="h-dvh w-full max-w-none gap-0 p-0 sm:w-[400px] sm:max-w-[400px]"
        @interact-outside="keepOpenOnCompanion"
        @escape-key-down="keepOpenOnEscape"
      >
        <SheetTitle class="sr-only">{{ t('terminal.snippets.title') }}</SheetTitle>
        <SheetDescription class="sr-only">{{ t('terminal.snippets.sheetIntro') }}</SheetDescription>
        <SnippetsPanel
          :can-paste="canPaste"
          closable
          @paste="(snippet: TermSnippet) => useSnippet(snippet, false)"
          @run="(snippet: TermSnippet) => useSnippet(snippet, true)"
          @add="addSnippet"
          @edit="editSnippet"
          @close="panelSheet = false"
        />
      </SheetContent>
    </Sheet>

    <NewTerminalSheet
      v-if="users"
      v-model:open="showNew"
      :users="users.users"
      :root-terminal="users.rootTerminal"
      :initial-user="newUser"
      @opened="opened"
    />
    <SnippetSheet v-model:open="showSnippet" :snippet="editing" />
  </div>
</template>
