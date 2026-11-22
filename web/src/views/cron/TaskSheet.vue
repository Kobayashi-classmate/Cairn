<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, defineAsyncComponent, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { isApiError, isStepUpRequired } from '@/api/client'
import { fetchPlans, type BackupPlan } from '@/api/backups'
import {
  SCRIPT_MAX,
  TASK_TYPES,
  TIMEOUT_DEFAULT,
  TIMEOUT_MAX,
  createTask,
  fetchCronUsers,
  updateTask,
  type CronTask,
  type TaskRequest,
  type TaskType,
} from '@/api/cron'
import type { TermUsers } from '@/api/terminal'
import FormField from '@/components/common/FormField.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import OptionGroup from '@/components/common/OptionGroup.vue'
import SelectField from '@/components/common/SelectField.vue'
import SwitchField from '@/components/common/SwitchField.vue'
import type { OptionItem, SelectOption } from '@/components/common/types'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { DEFAULT_PRESET, describeSchedule, isScheduleText, toExpression } from '@/cron/schedule'
import { isHttpUrl } from '@/forms/rules'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { formatBytes } from '@/i18n/format'
import { useCronStore } from '@/stores/cron'
import { useSessionStore } from '@/stores/session'
import ScheduleEditor from './ScheduleEditor.vue'

// "New task" / "Change task" (form sheet): a shell script run as a site
// owner or another user, a URL to fetch, or a backup plan to run, on a
// schedule. Each task becomes a systemd service and timer.
const CodeEditor = defineAsyncComponent(() => import('@/views/files/CodeEditor.vue'))

const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{
  /** The task to change, or null for a new one. */
  task: CronTask | null
  /** Values a new task starts with (for example a backup plan to schedule). */
  initial?: Partial<TaskRequest>
}>()
const emit = defineEmits<{ finished: [] }>()

const DEFAULT_SCRIPT = '#!/bin/sh\n\n'

const { t } = useI18n()
const store = useCronStore()
const session = useSessionStore()

const form = reactive({
  name: '',
  type: 'shell' as string,
  schedule: toExpression(DEFAULT_PRESET),
  user: '',
  script: DEFAULT_SCRIPT,
  url: '',
  plan: '',
  timeoutMin: String(TIMEOUT_DEFAULT),
  enabled: true,
})
type Field = 'name' | 'schedule' | 'user' | 'script' | 'url' | 'plan' | 'timeoutMin'
const errors = ref<Partial<Record<Field, string>>>({})
const editor = ref<{ getValue: () => string } | null>(null)
/** Bumped to give the script editor a fresh document. */
const scriptVersion = ref(0)
const submitting = ref(false)
const error = ref<unknown>(null)

const users = ref<TermUsers | null>(null)
const usersError = ref<unknown>(null)
const plans = ref<BackupPlan[] | null>(null)
const plansError = ref<unknown>(null)

const editing = computed(() => props.task !== null)
const canRoot = computed(() => session.can('cron.root'))

async function loadUsers() {
  try {
    users.value = await fetchCronUsers()
    usersError.value = null
    // The list's cached copy (which tasks this account may change) catches up.
    if (!canRoot.value) store.setUsers(users.value)
  } catch (err) {
    usersError.value = err
  }
}

async function loadPlans() {
  if (!session.can('backups.read')) {
    plans.value = []
    return
  }
  try {
    plans.value = await fetchPlans()
    plansError.value = null
  } catch (err) {
    plansError.value = err
  }
}

// Mounted on first use (already open), hence immediate.
watch(
  open,
  (value) => {
    if (!value) return
    const task = props.task
    const start = props.initial ?? {}
    Object.assign(form, {
      name: task?.name ?? start.name ?? '',
      type: task?.type ?? start.type ?? 'shell',
      schedule: task?.schedule ?? start.schedule ?? toExpression(DEFAULT_PRESET),
      user: task?.user ?? start.user ?? '',
      script: task?.script ?? start.script ?? DEFAULT_SCRIPT,
      url: task?.url ?? start.url ?? '',
      plan: task?.plan ?? start.plan ?? '',
      timeoutMin: String(task?.timeoutMin || start.timeoutMin || TIMEOUT_DEFAULT),
      enabled: task?.enabled ?? start.enabled ?? true,
    })
    scriptVersion.value++
    errors.value = {}
    error.value = null
    void loadUsers()
    void loadPlans()
  },
  { immediate: true },
)

const typeOptions = computed<OptionItem[]>(() =>
  TASK_TYPES.map((type) => ({ value: type, label: t(`cron.type.${type}`), hint: t(`cron.typeHint.${type}`) })),
)

const userOptions = computed<SelectOption[]>(() => {
  const list = users.value?.users ?? []
  const options: SelectOption[] = []
  for (const kind of ['site', 'user', 'root'] as const) {
    for (const user of list.filter((u) => u.kind === kind)) {
      options.push({
        value: user.name,
        label:
          kind === 'site' && user.site
            ? t('terminal.user.site', { user: user.name, site: user.site })
            : kind === 'root'
              ? t('cron.task.rootUser')
              : user.name,
        disabled: kind === 'root' && !canRoot.value,
      })
    }
  }
  // A task's user the list no longer has stays selectable.
  if (form.user && !list.some((user) => user.name === form.user)) options.push({ value: form.user, label: form.user })
  return options
})

const planOptions = computed<SelectOption[]>(() => (plans.value ?? []).map((plan) => ({ value: plan.id, label: plan.name })))
const isRoot = computed(() => form.type === 'shell' && form.user === 'root')
/** A login user who is not a site owner (and may be able to use sudo). */
const isOtherUser = computed(
  () => form.type === 'shell' && !isRoot.value && users.value?.users.some((user) => user.name === form.user && user.kind === 'user') === true,
)

function scriptOf(): string {
  return editor.value?.getValue() ?? form.script
}

/** The script lives in the editor; keep a copy so switching the type back does not lose it. */
function syncScript() {
  form.script = scriptOf()
}

function validate(script: string): boolean {
  const next: Partial<Record<Field, string>> = {}
  const name = form.name.trim()
  if (!name) next.name = validationMessage('name', 'required')
  else if ([...name].length > 64) next.name = validationMessage('name', 'max_length', { max: 64 })
  if (!isScheduleText(form.schedule)) next.schedule = validationMessage('schedule', 'schedule')
  if (form.type === 'shell') {
    if (!form.user) next.user = validationMessage('user', 'required')
    if (!script.trim()) next.script = validationMessage('script', 'required')
    else if (new TextEncoder().encode(script).length > SCRIPT_MAX) next.script = t('cron.task.scriptTooLarge', { size: formatBytes(SCRIPT_MAX) })
  }
  if (form.type === 'url') {
    const url = form.url.trim()
    if (!url) next.url = validationMessage('url', 'required')
    else if (!isHttpUrl(url) || url.length > 2048) next.url = validationMessage('url', 'http_url')
  }
  if (form.type === 'backup' && !form.plan) next.plan = validationMessage('plan', 'required')
  const timeout = Number(form.timeoutMin)
  if (!Number.isInteger(timeout) || timeout < 1 || timeout > TIMEOUT_MAX) {
    next.timeoutMin = validationMessage('timeoutMin', 'range', { min: 1, max: TIMEOUT_MAX })
  }
  errors.value = next
  return Object.keys(next).length === 0
}

const steps = computed(() => {
  const name = form.name.trim() || t('cron.task.namePlaceholder')
  const list = [editing.value ? t('cron.task.steps.update', { name }) : t('cron.task.steps.create', { name })]
  if (form.type === 'shell') list.push(t('cron.task.steps.shell', { user: form.user || t('cron.task.userPlaceholder') }))
  else if (form.type === 'url') list.push(t('cron.task.steps.url', { url: form.url.trim() || '—' }))
  else list.push(t('cron.task.steps.backup', { plan: planOptions.value.find((p) => p.value === form.plan)?.label ?? '—' }))
  list.push(
    form.enabled
      ? t('cron.task.steps.schedule', { schedule: describeSchedule(form.schedule) })
      : t('cron.task.steps.disabled'),
  )
  return list
})

async function submit() {
  // The script editor saves with Mod-S too: never a second request while one runs.
  if (submitting.value) return
  const script = form.type === 'shell' ? scriptOf() : ''
  if (!validate(script)) return
  const body: TaskRequest = {
    name: form.name.trim(),
    type: form.type as TaskType,
    schedule: form.schedule.trim(),
    timeoutMin: Number(form.timeoutMin),
    enabled: form.enabled,
    ...(form.type === 'shell' ? { user: form.user, script } : {}),
    ...(form.type === 'url' ? { url: form.url.trim() } : {}),
    ...(form.type === 'backup' ? { plan: form.plan } : {}),
  }
  submitting.value = true
  error.value = null
  try {
    const task = props.task
    // Tasks as root or other users than site owners are step-up protected:
    // the client asks for the password and retries.
    await store.runJob(() => (task ? updateTask(task.id, body) : createTask(body)), {
      rethrow: true,
      onFinished: () => emit('finished'),
    })
    open.value = false
  } catch (err) {
    if (isStepUpRequired(err)) return
    if (isApiError(err) && err.code === 'cron.plan_not_found') {
      // Deleted meanwhile: the plans are read again.
      errors.value = { ...errors.value, plan: errorMessage(err) }
      void loadPlans()
    } else error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <FormSheet
    v-model:open="open"
    :title="editing ? t('cron.task.editTitle', { name: task?.name ?? '' }) : t('cron.task.createTitle')"
    :description="t('cron.task.sheetIntro')"
    :submit-label="editing ? t('common.action.save') : t('cron.task.create')"
    :submitting="submitting"
    :steps="steps"
    @submit="submit"
  >
    <FormField id="task-name" :label="t('common.field.name')" :error="errors.name">
      <template #default="field">
        <Input v-bind="field" v-model="form.name" maxlength="64" autocomplete="off" />
      </template>
    </FormField>

    <div class="flex flex-col gap-1.5">
      <span id="task-type-label" class="text-detail font-medium">{{ t('common.field.type') }}</span>
      <OptionGroup v-model="form.type" :options="typeOptions" aria-labelledby="task-type-label" />
    </div>

    <div class="flex flex-col gap-1.5">
      <label for="task-schedule" class="text-detail font-medium">{{ t('common.field.schedule') }}</label>
      <ScheduleEditor id="task-schedule" v-model:expression="form.schedule" :invalid="!!errors.schedule" />
      <p v-if="errors.schedule" class="m-0 text-xs font-medium text-destructive">{{ errors.schedule }}</p>
    </div>

    <template v-if="form.type === 'shell'">
      <FormField id="task-user" :label="t('cron.task.user')" :hint="canRoot ? t('cron.task.userHint') : t('cron.task.userHintSites')" :error="errors.user">
        <template #default="field">
          <Skeleton v-if="!users && !usersError" class="h-9 w-full" />
          <SelectField v-else v-bind="field" v-model="form.user" :options="userOptions" :placeholder="t('cron.task.userPlaceholder')" />
        </template>
      </FormField>
      <Notice v-if="usersError" tone="destructive">{{ errorMessage(usersError) }}</Notice>
      <Notice v-if="isRoot" tone="warning">
        {{ t('cron.task.rootNote') }}
        <template v-if="users && !users.rootTerminal"> {{ t('cron.task.rootOff') }}</template>
      </Notice>
      <Notice v-else-if="isOtherUser" tone="warning">{{ t('cron.task.userNote') }}</Notice>
      <div class="flex flex-col gap-1.5">
        <span id="task-script-label" class="text-detail font-medium">{{ t('common.field.script') }}</span>
        <div class="h-60" role="group" aria-labelledby="task-script-label">
          <CodeEditor ref="editor" :key="scriptVersion" :value="form.script" path="task.sh" :label="t('common.field.script')" :autofocus="false" @changed="syncScript" @save="submit" />
        </div>
        <p class="m-0 text-xs text-muted-foreground">{{ t('cron.task.scriptHint', { size: formatBytes(SCRIPT_MAX) }) }}</p>
        <p v-if="errors.script" class="m-0 text-xs font-medium text-destructive">{{ errors.script }}</p>
      </div>
    </template>

    <FormField v-else-if="form.type === 'url'" id="task-url" :label="t('common.field.url')" :hint="t('cron.task.urlHint')" :error="errors.url">
      <template #default="field">
        <Input v-bind="field" v-model="form.url" type="url" autocomplete="off" spellcheck="false" autocapitalize="none" class="font-mono" />
      </template>
    </FormField>

    <template v-else>
      <FormField
        id="task-plan"
        :label="t('common.field.plan')"
        :hint="plans && plans.length === 0 ? t('cron.task.noPlans') : undefined"
        :error="errors.plan"
      >
        <template #default="field">
          <Skeleton v-if="!plans && !plansError" class="h-9 w-full" />
          <SelectField v-else v-bind="field" v-model="form.plan" :options="planOptions" :placeholder="t('cron.task.planPlaceholder')" />
        </template>
      </FormField>
      <Notice v-if="plansError" tone="destructive">{{ errorMessage(plansError) }}</Notice>
    </template>

    <FormField id="task-timeout" :label="t('cron.task.timeout')" :hint="t('cron.task.timeoutHint', { max: TIMEOUT_MAX })" :error="errors.timeoutMin">
      <template #default="field">
        <div class="flex items-center gap-2">
          <Input v-bind="field" v-model="form.timeoutMin" type="number" min="1" :max="TIMEOUT_MAX" inputmode="numeric" class="w-28 tabular-nums" />
          <span class="text-detail text-muted-foreground">{{ t('cron.schedule.minutesUnit') }}</span>
        </div>
      </template>
    </FormField>

    <SwitchField v-model="form.enabled" :label="t('cron.task.enabled')" :hint="t('cron.task.enabledHint')" />

    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
