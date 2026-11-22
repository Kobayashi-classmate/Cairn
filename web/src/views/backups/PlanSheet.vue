<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Database, Folder, Globe, Plus, X } from '@lucide/vue'
import {
  AGE_RECIPIENT,
  DEFAULT_RETENTION,
  createPlan,
  updatePlan,
  type BackupPlan,
  type BackupTarget,
  type PlanRequest,
  type Retention,
} from '@/api/backups'
import { fetchDatabases, type DbEngine } from '@/api/databases'
import { fetchSites, type SiteView } from '@/api/sites'
import { targetKey, targetLabel } from '@/backups/targets'
import DirBrowser from '@/components/common/DirBrowser.vue'
import FormField from '@/components/common/FormField.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import SelectField from '@/components/common/SelectField.vue'
import SwitchField from '@/components/common/SwitchField.vue'
import type { SelectOption } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { engineLabel } from '@/databases/labels'
import { rootLabel } from '@/files/paths'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { notifySuccess } from '@/lib/notify'
import { useBackupsStore } from '@/stores/backups'
import { useSessionStore } from '@/stores/session'

// "New plan" / "Change plan" (form sheet): what to back up (sites,
// databases, folders), where to keep the backups, whom to encrypt them for
// and how many to keep.
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{ plan: BackupPlan | null }>()
const emit = defineEmits<{ saved: [plan: BackupPlan] }>()

const MAX_TARGETS = 100
const MAX_RECIPIENTS = 16
const ROOTS = ['sites', 'backups', 'system'] as const
const RETENTION_KEYS = ['last', 'daily', 'weekly', 'monthly'] as const

const { t } = useI18n()
const store = useBackupsStore()
const session = useSessionStore()

const form = reactive({
  name: '',
  targets: [] as BackupTarget[],
  storageId: '',
  keepLocal: true,
  retention: { ...DEFAULT_RETENTION } as Retention,
  recipients: '',
})
type Field = 'name' | 'targets' | 'retention' | 'recipients'
const errors = ref<Partial<Record<Field, string>>>({})
const submitting = ref(false)
const error = ref<unknown>(null)

const sites = ref<SiteView[]>([])
const engines = ref<DbEngine[]>([])
const adding = ref<'site' | 'database' | 'directory' | null>(null)
const pickedSite = ref('')
const pickedDatabase = ref('')
const dirRoot = ref<string>('sites')
const dirPath = ref('')

const editing = computed(() => props.plan !== null)
const canSites = computed(() => session.can('sites.read'))
const canDatabases = computed(() => session.can('databases.read'))

watch(open, (value) => {
  if (!value) return
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
  errors.value = {}
  error.value = null
  loadSources()
})

function loadSources() {
  if (canSites.value) {
    fetchSites()
      .then((list) => (sites.value = list.sites))
      .catch(() => (sites.value = []))
  }
  if (canDatabases.value) {
    fetchDatabases()
      .then((status) => (engines.value = status.engines.filter((engine) => engine.kind !== 'redis')))
      .catch(() => (engines.value = []))
  }
}

const usedKeys = computed(() => new Set(form.targets.map(targetKey)))

const siteOptions = computed<SelectOption[]>(() =>
  sites.value.map((site) => ({
    value: site.code,
    label: site.domains.length ? `${site.domains[0]} (${site.code})` : site.code,
    disabled: usedKeys.value.has(`site-${site.code}`),
  })),
)

const databaseOptions = computed<SelectOption[]>(() =>
  engines.value.flatMap((engine) =>
    engine.databases
      .filter((db) => !db.missing)
      .map((db) => ({
        value: `${engine.id}/${db.name}`,
        label: `${db.name} (${engineLabel(engine)})`,
        disabled: usedKeys.value.has(`db-${engine.id}-${db.name}`),
      })),
  ),
)

const rootOptions = computed<SelectOption[]>(() => ROOTS.map((root) => ({ value: root, label: rootLabel(root) })))

function startAdding(kind: 'site' | 'database' | 'directory') {
  adding.value = kind
  pickedSite.value = ''
  pickedDatabase.value = ''
  dirRoot.value = 'sites'
  dirPath.value = ''
}

function addTarget(target: BackupTarget) {
  if (usedKeys.value.has(targetKey(target))) {
    errors.value = { ...errors.value, targets: validationMessage('targets', 'duplicate') }
    return
  }
  errors.value = { ...errors.value, targets: undefined }
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

function onRoot(root: string) {
  dirRoot.value = root
  dirPath.value = ''
}

const TARGET_ICON = { site: Globe, database: Database, directory: Folder } as const

const storageOptions = computed<SelectOption[]>(() => [
  { value: '', label: t('backups.plan.localOnly') },
  ...(store.storages ?? []).map((storage) => ({ value: storage.id, label: `${storage.name} (${storage.bucket})` })),
])

const recipientList = computed(() =>
  form.recipients
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean),
)

const keepLocal = computed({
  get: () => form.storageId === '' || form.keepLocal,
  set: (value: boolean) => (form.keepLocal = value),
})

function retentionValue(key: keyof Retention): string {
  return String(form.retention[key])
}

function setRetention(key: keyof Retention, value: string | number) {
  const n = Number(value)
  form.retention[key] = Number.isInteger(n) && n >= 0 ? Math.min(n, 1000) : 0
}

const retentionTotal = computed(() => RETENTION_KEYS.reduce((sum, key) => sum + form.retention[key], 0))

function validate(): boolean {
  const next: Partial<Record<Field, string>> = {}
  const name = form.name.trim()
  if (!name) next.name = validationMessage('name', 'required')
  else if ([...name].length > 64) next.name = validationMessage('name', 'max_length', { max: 64 })
  if (form.targets.length === 0 || form.targets.length > MAX_TARGETS) next.targets = validationMessage('targets', 'required')
  if (retentionTotal.value === 0) next.retention = validationMessage('retention', 'required')
  if (!recipientList.value.every((line) => AGE_RECIPIENT.test(line))) next.recipients = validationMessage('recipients', 'age_recipient')
  else if (recipientList.value.length > MAX_RECIPIENTS) next.recipients = validationMessage('recipients', 'count')
  errors.value = next
  return Object.keys(next).length === 0
}

async function submit() {
  if (!validate()) return
  const body: PlanRequest = {
    name: form.name.trim(),
    targets: form.targets,
    storageId: form.storageId || undefined,
    keepLocal: keepLocal.value,
    retention: form.retention,
    recipients: recipientList.value,
  }
  submitting.value = true
  error.value = null
  try {
    const saved = props.plan ? await updatePlan(props.plan.id, body) : await createPlan(body)
    notifySuccess(t('backups.plan.saved', { name: saved.name }))
    void store.load()
    emit('saved', saved)
    open.value = false
  } catch (err) {
    error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <FormSheet
    v-model:open="open"
    :title="editing ? t('backups.plan.editTitle', { name: plan?.name ?? '' }) : t('backups.plan.createTitle')"
    :description="t('backups.plan.sheetIntro')"
    :submit-label="editing ? t('common.action.save') : t('backups.plan.create')"
    :submitting="submitting"
    @submit="submit"
  >
    <FormField id="plan-name" :label="t('common.field.name')" :error="errors.name">
      <template #default="field">
        <Input v-bind="field" v-model="form.name" maxlength="64" autocomplete="off" />
      </template>
    </FormField>

    <fieldset class="m-0 flex min-w-0 flex-col gap-2 border-0 p-0">
      <legend class="mb-1.5 p-0 text-detail font-medium">{{ t('backups.plan.targets') }}</legend>
      <ul v-if="form.targets.length > 0" class="m-0 flex list-none flex-col divide-y rounded-md border p-0">
        <li v-for="(target, index) in form.targets" :key="targetKey(target)" class="flex items-center gap-2 py-1 ps-3 pe-1">
          <component :is="TARGET_ICON[target.kind]" class="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
          <span class="min-w-0 flex-1 text-detail [overflow-wrap:anywhere]" dir="auto">{{ targetLabel(target) }}</span>
          <Button type="button" variant="ghost" size="icon-sm" class="max-md:size-11" :aria-label="t('backups.plan.removeTarget', { target: targetLabel(target) })" @click="removeTarget(index)">
            <X aria-hidden="true" />
          </Button>
        </li>
      </ul>
      <p v-else class="m-0 text-xs text-muted-foreground">{{ t('backups.plan.noTargets') }}</p>

      <div v-if="adding" class="flex flex-col gap-3 rounded-md border border-dashed p-3">
        <SelectField
          v-if="adding === 'site'"
          v-model="pickedSite"
          :options="siteOptions"
          :placeholder="siteOptions.length ? t('backups.plan.pickSite') : t('backups.plan.noSites')"
          :aria-label="t('backups.plan.pickSite')"
        />
        <SelectField
          v-else-if="adding === 'database'"
          v-model="pickedDatabase"
          :options="databaseOptions"
          :placeholder="databaseOptions.length ? t('backups.plan.pickDatabase') : t('backups.plan.noDatabases')"
          :aria-label="t('backups.plan.pickDatabase')"
        />
        <template v-else>
          <SelectField :model-value="dirRoot" :options="rootOptions" :aria-label="t('backups.plan.pickRoot')" @update:model-value="onRoot" />
          <DirBrowser v-model:path="dirPath" :root="dirRoot" :label="t('backups.plan.pickFolder')" />
        </template>
        <div class="flex justify-end gap-2">
          <Button type="button" variant="ghost" size="sm" @click="adding = null">{{ t('common.action.cancel') }}</Button>
          <Button
            type="button"
            size="sm"
            :disabled="(adding === 'site' && !pickedSite) || (adding === 'database' && !pickedDatabase)"
            @click="confirmAdding"
          >
            {{ t('backups.plan.addTarget') }}
          </Button>
        </div>
      </div>
      <div v-else class="flex flex-wrap gap-2">
        <Button v-if="canSites" type="button" variant="outline" size="sm" @click="startAdding('site')">
          <Plus aria-hidden="true" />
          {{ t('backups.plan.addSite') }}
        </Button>
        <Button v-if="canDatabases" type="button" variant="outline" size="sm" @click="startAdding('database')">
          <Plus aria-hidden="true" />
          {{ t('backups.plan.addDatabase') }}
        </Button>
        <Button type="button" variant="outline" size="sm" @click="startAdding('directory')">
          <Plus aria-hidden="true" />
          {{ t('backups.plan.addDirectory') }}
        </Button>
      </div>
      <p v-if="errors.targets" class="m-0 text-xs font-medium text-destructive">{{ errors.targets }}</p>
    </fieldset>

    <FormField id="plan-storage" :label="t('backups.plan.storage')" :hint="(store.storages ?? []).length === 0 ? t('backups.plan.noStorage') : undefined">
      <template #default="field">
        <SelectField v-bind="field" v-model="form.storageId" :options="storageOptions" />
      </template>
    </FormField>
    <SwitchField v-model="keepLocal" :label="t('backups.plan.keepLocal')" :disabled="form.storageId === ''" />

    <fieldset class="m-0 flex min-w-0 flex-col gap-2 border-0 p-0">
      <legend class="mb-1.5 p-0 text-detail font-medium">{{ t('backups.plan.retention') }}</legend>
      <div class="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <label v-for="key in RETENTION_KEYS" :key="key" class="flex flex-col gap-1 text-xs text-muted-foreground">
          {{ t(`backups.retention.${key}`) }}
          <Input
            :model-value="retentionValue(key)"
            type="number"
            min="0"
            max="1000"
            step="1"
            inputmode="numeric"
            class="tabular-nums"
            @update:model-value="(value: string | number) => setRetention(key, value)"
          />
        </label>
      </div>
      <p class="m-0 text-xs text-muted-foreground">{{ t('backups.retention.explain', { ...form.retention }) }}</p>
      <p v-if="errors.retention" class="m-0 text-xs font-medium text-destructive">{{ errors.retention }}</p>
    </fieldset>

    <FormField id="plan-recipients" :label="t('backups.plan.encryption')" :hint="t('backups.plan.recipientsHint')" :error="errors.recipients">
      <template #default="field">
        <Textarea
          v-bind="field"
          v-model="form.recipients"
          rows="3"
          class="font-mono text-xs"
          spellcheck="false"
          autocomplete="off"
          :placeholder="t('backups.plan.recipientsPlaceholder')"
        />
      </template>
    </FormField>
    <Notice v-if="recipientList.length > 0" tone="warning">{{ t('backups.plan.keyWarning') }}</Notice>
    <Notice v-else-if="form.storageId" tone="warning">{{ t('backups.plan.encryptSuggest') }}</Notice>

    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
