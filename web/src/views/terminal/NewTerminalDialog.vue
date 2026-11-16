<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Opens a terminal as a site owner, an ordinary user or (administrators,
// when switched on) root. Opening one asks for the password again.
import { computed, h, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NFormItem, NModal, NSelect, NTag, type SelectOption, type SelectGroupOption } from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import { openTermSession, type TermSession, type TermUser } from '@/api/terminal'
import { errorMessage } from '@/i18n/errors'

const show = defineModel<boolean>('show', { required: true })
const props = defineProps<{
  users: TermUser[]
  /** Root terminals are switched on. */
  rootTerminal: boolean
}>()
const emit = defineEmits<{ opened: [session: TermSession] }>()

/** Size the shell starts with; the terminal reports its real size once shown. */
const START_COLS = 120
const START_ROWS = 32

const { t } = useI18n()
const user = ref<string | null>(null)
const submitting = ref(false)
const error = ref<unknown>(null)

const selected = computed(() => props.users.find((entry) => entry.name === user.value) ?? null)

watch(show, (open) => {
  if (!open) return
  error.value = null
  const first = props.users.find((entry) => entry.kind !== 'root')
  if (!user.value || !props.users.some((entry) => entry.name === user.value)) user.value = first?.name ?? null
})

function option(entry: TermUser): SelectOption {
  const disabled = entry.kind === 'root' && !props.rootTerminal
  return {
    value: entry.name,
    label: entry.kind === 'site' && entry.site ? t('terminal.user.site', { user: entry.name, site: entry.site }) : entry.name,
    disabled,
    kind: entry.kind,
  }
}

const options = computed<(SelectOption | SelectGroupOption)[]>(() => {
  const groups: (SelectOption | SelectGroupOption)[] = []
  for (const kind of ['site', 'user', 'root'] as const) {
    const children = props.users.filter((entry) => entry.kind === kind).map(option)
    if (children.length > 0) groups.push({ type: 'group', key: kind, label: t(`terminal.user.kind.${kind}`), children })
  }
  return groups
})

function renderLabel(item: SelectOption | SelectGroupOption) {
  if (item.type === 'group' || item.kind !== 'root') return String(item.label ?? '')
  return h('span', { class: 'root-option' }, [
    String(item.label ?? ''),
    h(NTag, { size: 'small', type: 'error', bordered: false }, () =>
      props.rootTerminal ? t('terminal.user.dangerous') : t('terminal.user.rootOff'),
    ),
  ])
}

async function submit() {
  if (!user.value) return
  submitting.value = true
  error.value = null
  try {
    // Step-up protected: the client asks for the password and retries.
    emit('opened', await openTermSession(user.value, START_COLS, START_ROWS))
    show.value = false
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <NModal v-model:show="show" preset="card" :title="t('terminal.new.title')" class="new-terminal">
    <NFormItem :label="t('terminal.new.user')" :show-feedback="false">
      <NSelect v-model:value="user" :options="options" :render-label="renderLabel" filterable />
    </NFormItem>
    <p v-if="selected" class="home" dir="ltr">{{ t('terminal.new.home', { home: selected.home }) }}</p>
    <NAlert v-if="selected?.kind === 'root'" type="error" class="block">{{ t('terminal.new.rootWarning') }}</NAlert>
    <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>
    <div class="actions">
      <NButton :disabled="submitting" @click="show = false">{{ t('common.action.cancel') }}</NButton>
      <NButton
        :type="selected?.kind === 'root' ? 'error' : 'primary'"
        :loading="submitting"
        :disabled="!selected"
        @click="submit"
      >
        {{ t('terminal.new.submit') }}
      </NButton>
    </div>
  </NModal>
</template>

<style scoped>
.new-terminal {
  inline-size: min(480px, calc(100vw - 32px));
}

.home {
  margin: 8px 0 0;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  opacity: 0.7;
}

.block {
  margin-block-start: 12px;
}

.actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-block-start: 16px;
}

:deep(.root-option) {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}
</style>
