<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Creates or changes a scheduled task: a shell script run as a site owner
// or another user, a URL to fetch, or a backup plan to run.
import { computed, defineAsyncComponent, nextTick, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NForm,
  NFormItem,
  NInput,
  NInputNumber,
  NModal,
  NRadioButton,
  NRadioGroup,
  NSelect,
  NSwitch,
  NText,
  type FormInst,
  type FormRules,
  type SelectGroupOption,
  type SelectOption,
} from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
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
import { fetchPlans, type BackupPlan } from '@/api/backups'
import type { Job } from '@/api/jobs'
import type { TermUsers } from '@/api/terminal'
import { toExpression, DEFAULT_PRESET } from '@/cron/schedule'
import { checkRule, isHttpUrl, requiredRule } from '@/forms/rules'
import { errorMessage } from '@/i18n/errors'
import { formatBytes } from '@/i18n/format'
import { useSessionStore } from '@/stores/session'
import ScheduleEditor from './ScheduleEditor.vue'

// The script editor (CodeMirror) is loaded when the dialog needs it.
const CodeEditor = defineAsyncComponent(() => import('@/views/files/CodeEditor.vue'))

const show = defineModel<boolean>('show', { required: true })
const props = defineProps<{
  /** The task to change, or null for a new one. */
  task: CronTask | null
  /** Values a new task starts with (for example a backup plan to schedule). */
  initial?: Partial<TaskRequest>
}>()
const emit = defineEmits<{ started: [job: Job] }>()

const DEFAULT_SCRIPT = '#!/bin/sh\n\n'

const { t } = useI18n()
const session = useSessionStore()
const formRef = ref<FormInst | null>(null)
const editor = ref<{ getValue: () => string } | null>(null)
const form = reactive({
  name: '',
  type: 'shell' as TaskType,
  schedule: toExpression(DEFAULT_PRESET),
  user: null as string | null,
  script: DEFAULT_SCRIPT,
  url: '',
  plan: null as string | null,
  timeoutMin: TIMEOUT_DEFAULT as number | null,
  enabled: true,
})
/** Bumped to give the script editor a fresh document. */
const scriptVersion = ref(0)
const scriptError = ref('')
const submitting = ref(false)
const error = ref<unknown>(null)

const users = ref<TermUsers | null>(null)
const usersError = ref<unknown>(null)
const plans = ref<BackupPlan[]>([])
const plansError = ref<unknown>(null)

const editing = computed(() => props.task !== null)
const canRoot = computed(() => session.can('cron.root'))

async function loadUsers() {
  try {
    users.value = await fetchCronUsers()
    usersError.value = null
  } catch (err) {
    usersError.value = err
  }
}

async function loadPlans() {
  try {
    plans.value = await fetchPlans()
    plansError.value = null
  } catch (err) {
    plansError.value = err
  }
}

watch(show, async (open) => {
  if (!open) return
  const task = props.task
  const start = props.initial ?? {}
  Object.assign(form, {
    name: task?.name ?? start.name ?? '',
    type: task?.type ?? start.type ?? 'shell',
    schedule: task?.schedule ?? start.schedule ?? toExpression(DEFAULT_PRESET),
    user: task?.user ?? start.user ?? null,
    script: task?.script ?? start.script ?? DEFAULT_SCRIPT,
    url: task?.url ?? start.url ?? '',
    plan: task?.plan ?? start.plan ?? null,
    timeoutMin: task?.timeoutMin || start.timeoutMin || TIMEOUT_DEFAULT,
    enabled: task?.enabled ?? start.enabled ?? true,
  })
  scriptVersion.value++
  scriptError.value = ''
  error.value = null
  await nextTick()
  formRef.value?.restoreValidation()
  void loadUsers()
  void loadPlans()
})

const typeOptions = computed(() => TASK_TYPES.map((type) => ({ value: type, label: t(`cron.type.${type}`) })))

const userOptions = computed<(SelectOption | SelectGroupOption)[]>(() => {
  const list = users.value?.users ?? []
  const groups: (SelectOption | SelectGroupOption)[] = []
  for (const kind of ['site', 'user', 'root'] as const) {
    const children = list
      .filter((user) => user.kind === kind)
      .map((user) => ({
        value: user.name,
        label: kind === 'site' && user.site ? t('terminal.user.site', { user: user.name, site: user.site }) : user.name,
        disabled: kind === 'root' && !canRoot.value,
      }))
    if (children.length > 0) groups.push({ type: 'group', key: kind, label: t(`terminal.user.kind.${kind}`), children })
  }
  // A task's user the list no longer has stays selectable.
  if (form.user && !list.some((user) => user.name === form.user)) groups.push({ value: form.user, label: form.user })
  return groups
})

const planOptions = computed(() => plans.value.map((plan) => ({ value: plan.id, label: plan.name })))
const isRoot = computed(() => form.type === 'shell' && form.user === 'root')

const rules = computed<FormRules>(() => ({
  name: [requiredRule('name'), checkRule('name', 'max_length', (v) => [...v.trim()].length <= 64, { max: 64 })],
  user: form.type === 'shell' ? [{ required: true, trigger: ['change', 'blur'], message: t('errors.validation.rule.required', { field: t('common.field.user') }) }] : [],
  url:
    form.type === 'url'
      ? [requiredRule('url'), checkRule('url', 'http_url', (v) => isHttpUrl(v.trim()) && v.trim().length <= 2048)]
      : [],
  plan: form.type === 'backup' ? [{ required: true, trigger: ['change', 'blur'], message: t('errors.validation.rule.required', { field: t('common.field.plan') }) }] : [],
}))

function scriptOf(): string {
  return editor.value?.getValue() ?? form.script
}

async function submit() {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  const script = form.type === 'shell' ? scriptOf() : ''
  scriptError.value = ''
  if (form.type === 'shell') {
    if (!script.trim()) {
      scriptError.value = t('errors.validation.rule.required', { field: t('common.field.script') })
      return
    }
    if (new TextEncoder().encode(script).length > SCRIPT_MAX) {
      scriptError.value = t('cron.task.scriptTooLarge', { size: formatBytes(SCRIPT_MAX) })
      return
    }
  }
  const body: TaskRequest = {
    name: form.name.trim(),
    type: form.type,
    schedule: form.schedule.trim(),
    timeoutMin: form.timeoutMin ?? TIMEOUT_DEFAULT,
    enabled: form.enabled,
    ...(form.type === 'shell' ? { user: form.user ?? '', script } : {}),
    ...(form.type === 'url' ? { url: form.url.trim() } : {}),
    ...(form.type === 'backup' ? { plan: form.plan ?? '' } : {}),
  }
  submitting.value = true
  error.value = null
  try {
    // Root tasks are step-up protected: the client asks for the password and retries.
    const job = props.task ? await updateTask(props.task.id, body) : await createTask(body)
    emit('started', job)
    show.value = false
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <NModal
    v-model:show="show"
    preset="card"
    :title="editing ? t('cron.task.editTitle', { name: task?.name ?? '' }) : t('cron.task.createTitle')"
    class="task-dialog"
    :mask-closable="false"
  >
    <NForm ref="formRef" :model="form" :rules="rules" :disabled="submitting" label-placement="top">
      <NFormItem path="name" :label="t('common.field.name')">
        <NInput v-model:value="form.name" :maxlength="64" :input-props="{ autocomplete: 'off' }" />
      </NFormItem>
      <NFormItem :label="t('common.field.type')" :show-feedback="false" class="gap">
        <NRadioGroup v-model:value="form.type">
          <NRadioButton v-for="option in typeOptions" :key="option.value" :value="option.value">{{ option.label }}</NRadioButton>
        </NRadioGroup>
      </NFormItem>
      <NFormItem path="schedule" :label="t('common.field.schedule')">
        <ScheduleEditor v-model:expression="form.schedule" />
      </NFormItem>

      <template v-if="form.type === 'shell'">
        <NFormItem path="user" :label="t('cron.task.user')">
          <div class="field">
            <NSelect v-model:value="form.user" :options="userOptions" filterable :placeholder="t('cron.task.userPlaceholder')" />
            <NText v-if="usersError" type="error" class="hint">{{ errorMessage(usersError) }}</NText>
          </div>
        </NFormItem>
        <NAlert v-if="isRoot" type="warning" class="block">
          {{ t('cron.task.rootNote') }}
          <template v-if="users && !users.rootTerminal"> {{ t('cron.task.rootOff') }}</template>
        </NAlert>
        <NFormItem :label="t('common.field.script')" :feedback="scriptError" :validation-status="scriptError ? 'error' : undefined">
          <div class="field">
            <div class="script">
              <CodeEditor ref="editor" :key="scriptVersion" :value="form.script" path="task.sh" :autofocus="false" @save="submit" />
            </div>
            <NText depth="3" class="hint">{{ t('cron.task.scriptHint', { size: formatBytes(SCRIPT_MAX) }) }}</NText>
          </div>
        </NFormItem>
      </template>

      <NFormItem v-else-if="form.type === 'url'" path="url" :label="t('common.field.url')">
        <div class="field">
          <NInput v-model:value="form.url" :input-props="{ type: 'url', autocomplete: 'off', spellcheck: false }" />
          <NText depth="3" class="hint">{{ t('cron.task.urlHint') }}</NText>
        </div>
      </NFormItem>

      <NFormItem v-else path="plan" :label="t('common.field.plan')">
        <div class="field">
          <NSelect v-model:value="form.plan" :options="planOptions" :placeholder="t('cron.task.planPlaceholder')" />
          <NText v-if="plansError" type="error" class="hint">{{ errorMessage(plansError) }}</NText>
          <NText v-else-if="plans.length === 0" depth="3" class="hint">{{ t('cron.task.noPlans') }}</NText>
        </div>
      </NFormItem>

      <div class="row">
        <NFormItem :label="t('cron.task.timeout')" :show-feedback="false">
          <NInputNumber v-model:value="form.timeoutMin" :min="1" :max="TIMEOUT_MAX" :precision="0" class="timeout">
            <template #suffix>{{ t('cron.schedule.minutesUnit') }}</template>
          </NInputNumber>
        </NFormItem>
        <NFormItem :label="t('cron.task.enabled')" :show-feedback="false">
          <NSwitch v-model:value="form.enabled" />
        </NFormItem>
      </div>
    </NForm>
    <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>
    <div class="actions">
      <NButton :disabled="submitting" @click="show = false">{{ t('common.action.cancel') }}</NButton>
      <NButton :type="isRoot ? 'warning' : 'primary'" :loading="submitting" @click="submit">{{ t('common.action.save') }}</NButton>
    </div>
  </NModal>
</template>

<style scoped>
.task-dialog {
  inline-size: min(760px, calc(100vw - 32px));
}

.field {
  display: flex;
  flex-direction: column;
  gap: 6px;
  inline-size: 100%;
}

.hint {
  font-size: 12px;
}

.gap {
  margin-block-end: 16px;
}

.script {
  block-size: 260px;
}

.row {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 32px;
}

.timeout {
  inline-size: 160px;
}

.block {
  margin-block: 0 12px;
}

.actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-block-start: 16px;
}
</style>
