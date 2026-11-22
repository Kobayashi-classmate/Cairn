<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Search } from '@lucide/vue'
import { isStepUpRequired } from '@/api/client'
import { openTermSession, type TermSession, type TermUser, type TermUserKind } from '@/api/terminal'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import OptionGroup from '@/components/common/OptionGroup.vue'
import type { OptionItem } from '@/components/common/types'
import { errorMessage } from '@/i18n/errors'
import { userLabel } from './labels'

// "New terminal" (form sheet, step-up protected): whom the shell runs as —
// a site owner, an ordinary user or (administrators, when switched on)
// root — and where it starts.
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{
  users: TermUser[]
  /** Root terminals are switched on. */
  rootTerminal: boolean
  /** Who to pick first (opening one like an ended terminal). */
  initialUser?: string
}>()
const emit = defineEmits<{ opened: [session: TermSession] }>()

/** Size the shell starts with; the terminal reports its real size once shown. */
const START_COLS = 120
const START_ROWS = 32
/** From this many people a filter helps. */
const FILTER_FROM = 8

const { t } = useI18n()
const user = ref('')
const filter = ref('')
const submitting = ref(false)
const error = ref<unknown>(null)

const selected = computed(() => props.users.find((entry) => entry.name === user.value) ?? null)

function selectable(entry: TermUser): boolean {
  return entry.kind !== 'root' || props.rootTerminal
}

watch(open, (value) => {
  if (!value) return
  error.value = null
  filter.value = ''
  const wanted = props.users.find((entry) => entry.name === props.initialUser && selectable(entry))
  if (wanted) user.value = wanted.name
  else if (!selected.value || !selectable(selected.value)) user.value = props.users.find((entry) => entry.kind !== 'root')?.name ?? ''
})

const groups = computed(() => {
  const query = filter.value.trim().toLowerCase()
  const result: { kind: TermUserKind; options: OptionItem[] }[] = []
  for (const kind of ['site', 'user', 'root'] as const) {
    const options = props.users
      .filter((entry) => entry.kind === kind)
      .filter((entry) => !query || `${entry.name} ${entry.site ?? ''} ${entry.home}`.toLowerCase().includes(query))
      .map((entry) => ({
        value: entry.name,
        label: userLabel(entry.name, entry.site),
        hint: entry.kind === 'root' && !props.rootTerminal ? t('terminal.user.rootOff') : entry.home,
        disabled: !selectable(entry),
      }))
    if (options.length > 0) result.push({ kind, options })
  }
  return result
})

const steps = computed(() => (selected.value ? [t('terminal.new.step', { user: selected.value.name, home: selected.value.home })] : []))

async function submit() {
  if (!selected.value) return
  submitting.value = true
  error.value = null
  try {
    // Step-up protected: the client asks for the password and retries.
    emit('opened', await openTermSession(selected.value.name, START_COLS, START_ROWS))
    open.value = false
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <FormSheet
    v-model:open="open"
    :title="t('terminal.new.title')"
    :description="t('terminal.new.intro')"
    :submit-label="t('terminal.new.submit')"
    :submitting="submitting"
    :submit-disabled="!selected"
    :steps="steps"
    @submit="submit"
  >
    <label
      v-if="users.length >= FILTER_FROM"
      class="flex h-9 items-center gap-2 rounded-md border border-input bg-card px-2.5 text-muted-foreground focus-within:border-ring focus-within:ring-2 focus-within:ring-ring/50 max-md:h-11"
    >
      <Search class="size-4 shrink-0" aria-hidden="true" />
      <input
        v-model="filter"
        type="search"
        :aria-label="t('terminal.new.filter')"
        :placeholder="t('terminal.new.filter')"
        class="h-full min-w-0 flex-1 bg-transparent text-base text-foreground outline-none placeholder:text-muted-foreground md:text-sm"
      />
    </label>
    <p v-if="users.length === 0" class="m-0 text-detail text-muted-foreground">{{ t('terminal.new.nobody') }}</p>
    <section v-for="group in groups" :key="group.kind" class="flex flex-col gap-2">
      <h3 :id="`term-kind-${group.kind}`" class="m-0 text-caption text-muted-foreground">{{ t(`terminal.user.kind.${group.kind}`) }}</h3>
      <OptionGroup v-model="user" :options="group.options" :aria-labelledby="`term-kind-${group.kind}`" />
    </section>
    <Notice v-if="selected?.kind === 'root'" tone="destructive">{{ t('terminal.new.rootWarning') }}</Notice>
    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
