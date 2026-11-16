<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Creates or changes a backup plan: what to back up, where to, whom to
// encrypt for and how many backups to keep.
import { computed, nextTick, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NForm,
  NFormItem,
  NIcon,
  NInput,
  NInputNumber,
  NModal,
  NSelect,
  NSwitch,
  NTag,
  NText,
  type FormInst,
  type FormRules,
  type SelectGroupOption,
  type SelectOption,
} from 'naive-ui'
import { CloseOutline } from '@vicons/ionicons5'
import {
  AGE_RECIPIENT,
  DEFAULT_RETENTION,
  createPlan,
  updatePlan,
  type BackupPlan,
  type BackupTarget,
  type PlanRequest,
  type Retention,
  type StorageView,
} from '@/api/backups'
import { fetchDatabases, type DbEngine } from '@/api/databases'
import { fetchSites, type SiteView } from '@/api/sites'
import { targetKey, targetLabel } from '@/backups/targets'
import { engineLabel } from '@/databases/labels'
import { rootLabel } from '@/files/paths'
import { checkRule, requiredRule } from '@/forms/rules'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { useSessionStore } from '@/stores/session'
import DirPicker from '@/views/files/DirPicker.vue'

const show = defineModel<boolean>('show', { required: true })
const props = defineProps<{ plan: BackupPlan | null; storages: StorageView[] }>()
const emit = defineEmits<{ saved: [plan: BackupPlan] }>()

const MAX_TARGETS = 100
const MAX_RECIPIENTS = 16
const ROOTS = ['sites', 'backups', 'system'] as const
const RETENTION_KEYS = ['last', 'daily', 'weekly', 'monthly'] as const

const { t } = useI18n()
const session = useSessionStore()
const formRef = ref<FormInst | null>(null)
const form = reactive({
  name: '',
  targets: [] as BackupTarget[],
  storageId: '',
  keepLocal: true,
  retention: { ...DEFAULT_RETENTION } as Retention,
  recipients: '',
})
const submitting = ref(false)
const error = ref<unknown>(null)
const targetsError = ref('')

// Sources of targets.
const sites = ref<SiteView[]>([])
const engines = ref<DbEngine[]>([])
const adding = ref<'site' | 'database' | 'directory' | null>(null)
const pickedSite = ref<string | null>(null)
const pickedDatabase = ref<string | null>(null)
const dirRoot = ref<string>('sites')
const dirPath = ref('')

const editing = computed(() => props.plan !== null)

watch(show, async (open) => {
  if (!open) return
  const plan = props.plan
  Object.assign(form, {
    name: plan?.name ?? '',
    targets: (plan?.targets ?? []).map((target) => ({ ...target })),
    storageId: plan?.storageId ?? '',
    keepLocal: plan?.keepLocal ?? true,
    retention: { ...(plan?.retention ?? DEFAULT_RETENTION) },
    recipients: (plan?.recipients ?? []).join('\n'),
  })
  adding.value = null
  error.value = null
  targetsError.value = ''
  await nextTick()
  formRef.value?.restoreValidation()
  void loadSources()
})

async function loadSources() {
  if (session.can('sites.read')) {
    fetchSites()
      .then((list) => (sites.value = list.sites))
      .catch(() => (sites.value = []))
  }
  if (session.can('databases.read')) {
    fetchDatabases()
      .then((status) => (engines.value = status.engines.filter((engine) => engine.kind !== 'redis')))
      .catch(() => (engines.value = []))
  }
}

const usedKeys = computed(() => new Set(form.targets.map(targetKey)))

const siteOptions = computed<SelectOption[]>(() =>
  sites.value.map((site) => ({
    value: site.code,
    label: site.domains.length ? `${site.code} (${site.domains[0]})` : site.code,
    disabled: usedKeys.value.has(`site-${site.code}`),
  })),
)

const databaseOptions = computed<SelectGroupOption[]>(() =>
  engines.value
    .filter((engine) => engine.databases.length > 0)
    .map((engine) => ({
      type: 'group' as const,
      key: engine.id,
      label: engineLabel(engine),
      children: engine.databases
        .filter((db) => !db.missing)
        .map((db) => ({
          value: `${engine.id}/${db.name}`,
          label: db.name,
          disabled: usedKeys.value.has(`db-${engine.id}-${db.name}`),
        })),
    })),
)

const rootOptions = computed(() => ROOTS.map((root) => ({ value: root, label: rootLabel(root) })))

function startAdding(kind: 'site' | 'database' | 'directory') {
  adding.value = kind
  pickedSite.value = null
  pickedDatabase.value = null
  dirRoot.value = 'sites'
  dirPath.value = ''
}

function addTarget(target: BackupTarget) {
  targetsError.value = ''
  if (usedKeys.value.has(targetKey(target))) {
    targetsError.value = validationMessage('targets', 'duplicate')
    return
  }
  form.targets.push(target)
  adding.value = null
}

function confirmAdding() {
  if (adding.value === 'site' && pickedSite.value) addTarget({ kind: 'site', site: pickedSite.value })
  else if (adding.value === 'database' && pickedDatabase.value) {
    const [engine = '', database = ''] = pickedDatabase.value.split('/')
    addTarget({ kind: 'database', engine, database })
  } else if (adding.value === 'directory') addTarget({ kind: 'directory', root: dirRoot.value, path: dirPath.value })
}

function removeTarget(index: number) {
  form.targets.splice(index, 1)
}

const storageOptions = computed(() => [
  { value: '', label: t('backups.plan.localOnly') },
  ...props.storages.map((storage) => ({ value: storage.id, label: `${storage.name} (${storage.bucket})` })),
])

const recipientList = computed(() =>
  form.recipients
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean),
)

const rules = computed<FormRules>(() => ({
  name: [requiredRule('name'), checkRule('name', 'max_length', (v) => [...v.trim()].length <= 64, { max: 64 })],
  recipients: [
    checkRule('recipients', 'age_recipient', () => recipientList.value.every((line) => AGE_RECIPIENT.test(line))),
    checkRule('recipients', 'count', () => recipientList.value.length <= MAX_RECIPIENTS),
  ],
}))

const retentionTotal = computed(() => form.retention.last + form.retention.daily + form.retention.weekly + form.retention.monthly)

async function submit() {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  targetsError.value = ''
  if (form.targets.length === 0 || form.targets.length > MAX_TARGETS) {
    targetsError.value = validationMessage('targets', 'required')
    return
  }
  if (retentionTotal.value === 0) return
  const body: PlanRequest = {
    name: form.name.trim(),
    targets: form.targets,
    storageId: form.storageId || undefined,
    keepLocal: form.storageId === '' || form.keepLocal,
    retention: form.retention,
    recipients: recipientList.value,
  }
  submitting.value = true
  error.value = null
  try {
    const saved = props.plan ? await updatePlan(props.plan.id, body) : await createPlan(body)
    emit('saved', saved)
    show.value = false
  } catch (err) {
    error.value = err
  } finally {
    submitting.value = false
  }
}

function setRetention(key: keyof Retention, value: number | null) {
  form.retention[key] = value ?? 0
}
</script>

<template>
  <NModal
    v-model:show="show"
    preset="card"
    :title="editing ? t('backups.plan.editTitle', { name: plan?.name ?? '' }) : t('backups.plan.createTitle')"
    class="plan-dialog"
    :mask-closable="false"
  >
    <NForm ref="formRef" :model="form" :rules="rules" :disabled="submitting" label-placement="top">
      <NFormItem path="name" :label="t('common.field.name')">
        <NInput v-model:value="form.name" :maxlength="64" :input-props="{ autocomplete: 'off' }" />
      </NFormItem>

      <NFormItem :label="t('backups.plan.targets')" :feedback="targetsError" :validation-status="targetsError ? 'error' : undefined">
        <div class="field">
          <ul v-if="form.targets.length > 0" class="targets">
            <li v-for="(target, index) in form.targets" :key="targetKey(target)" class="target">
              <NTag size="small" :bordered="false">{{ t(`backups.targetKind.${target.kind}`) }}</NTag>
              <span class="target__label" dir="auto">{{ targetLabel(target) }}</span>
              <NButton size="tiny" quaternary circle :aria-label="t('common.action.delete')" @click="removeTarget(index)">
                <template #icon>
                  <NIcon><CloseOutline /></NIcon>
                </template>
              </NButton>
            </li>
          </ul>
          <NText v-else depth="3" class="hint">{{ t('backups.plan.noTargets') }}</NText>

          <div v-if="adding" class="adding">
            <NSelect
              v-if="adding === 'site'"
              v-model:value="pickedSite"
              :options="siteOptions"
              filterable
              :placeholder="t('backups.plan.pickSite')"
            />
            <NSelect
              v-else-if="adding === 'database'"
              v-model:value="pickedDatabase"
              :options="databaseOptions"
              filterable
              :placeholder="t('backups.plan.pickDatabase')"
            />
            <div v-else class="adding__dir">
              <NSelect v-model:value="dirRoot" :options="rootOptions" class="adding__root" @update:value="dirPath = ''" />
              <DirPicker v-model:path="dirPath" :root="dirRoot" />
            </div>
            <div class="adding__actions">
              <NButton size="small" @click="adding = null">{{ t('common.action.cancel') }}</NButton>
              <NButton
                size="small"
                type="primary"
                :disabled="(adding === 'site' && !pickedSite) || (adding === 'database' && !pickedDatabase)"
                @click="confirmAdding"
              >
                {{ t('backups.plan.addTarget') }}
              </NButton>
            </div>
          </div>
          <div v-else class="add">
            <NButton size="small" :disabled="!session.can('sites.read')" @click="startAdding('site')">{{ t('backups.plan.addSite') }}</NButton>
            <NButton size="small" :disabled="!session.can('databases.read')" @click="startAdding('database')">
              {{ t('backups.plan.addDatabase') }}
            </NButton>
            <NButton size="small" @click="startAdding('directory')">{{ t('backups.plan.addDirectory') }}</NButton>
          </div>
        </div>
      </NFormItem>

      <NFormItem :label="t('backups.plan.storage')" :show-feedback="false" class="gap">
        <div class="field">
          <NSelect v-model:value="form.storageId" :options="storageOptions" />
          <label class="switch">
            <NSwitch :value="form.storageId === '' || form.keepLocal" :disabled="form.storageId === ''" @update:value="(on: boolean) => (form.keepLocal = on)" />
            <span>{{ t('backups.plan.keepLocal') }}</span>
          </label>
          <NText v-if="storages.length === 0" depth="3" class="hint">{{ t('backups.plan.noStorage') }}</NText>
        </div>
      </NFormItem>

      <NFormItem :label="t('backups.plan.retention')" :feedback="retentionTotal === 0 ? validationMessage('retention', 'required') : ''" :validation-status="retentionTotal === 0 ? 'error' : undefined">
        <div class="field">
          <div class="retention">
            <label v-for="key in RETENTION_KEYS" :key="key" class="retention__item">
              <span>{{ t(`backups.retention.${key}`) }}</span>
              <NInputNumber
                :value="form.retention[key]"
                :min="0"
                :max="1000"
                :precision="0"
                size="small"
                @update:value="(value: number | null) => setRetention(key, value)"
              />
            </label>
          </div>
          <NText depth="3" class="hint">{{ t('backups.retention.explain', { ...form.retention }) }}</NText>
        </div>
      </NFormItem>

      <NFormItem path="recipients" :label="t('backups.plan.encryption')">
        <div class="field">
          <NInput
            v-model:value="form.recipients"
            type="textarea"
            :autosize="{ minRows: 2, maxRows: 6 }"
            class="mono"
            :placeholder="t('backups.plan.recipientsPlaceholder')"
            :input-props="{ spellcheck: false, autocomplete: 'off' }"
          />
          <NText depth="3" class="hint">{{ t('backups.plan.recipientsHint') }}</NText>
        </div>
      </NFormItem>
      <NAlert v-if="recipientList.length > 0" type="warning" class="block">{{ t('backups.plan.keyWarning') }}</NAlert>
      <NAlert v-else-if="form.storageId" type="warning" class="block">{{ t('backups.plan.encryptSuggest') }}</NAlert>
    </NForm>
    <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>
    <div class="actions">
      <NButton :disabled="submitting" @click="show = false">{{ t('common.action.cancel') }}</NButton>
      <NButton type="primary" :loading="submitting" @click="submit">{{ t('common.action.save') }}</NButton>
    </div>
  </NModal>
</template>

<style scoped>
.plan-dialog {
  inline-size: min(720px, calc(100vw - 32px));
}

.field {
  display: flex;
  flex-direction: column;
  gap: 8px;
  inline-size: 100%;
}

.hint {
  font-size: 12px;
}

.gap {
  margin-block-end: 16px;
}

.targets {
  margin: 0;
  padding: 0;
  list-style: none;
}

.target {
  display: flex;
  align-items: center;
  gap: 8px;
  padding-block: 3px;
}

.target__label {
  flex: 1;
  min-inline-size: 0;
  overflow-wrap: anywhere;
}

.add,
.adding__actions {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.adding {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 10px;
  border: 1px dashed rgba(128, 128, 128, 0.4);
  border-radius: 4px;
}

.adding__dir {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.adding__root {
  max-inline-size: 240px;
}

.adding__actions {
  justify-content: flex-end;
}

.switch {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
}

.retention {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 16px;
}

.retention__item {
  display: flex;
  flex-direction: column;
  gap: 4px;
  inline-size: 120px;
  font-size: 13px;
}

.block {
  margin-block: 0 12px;
}

.actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-block-start: 8px;
}

.mono :deep(textarea) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
}
</style>
