<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Eye, LoaderCircle } from '@lucide/vue'
import { isStepUpRequired } from '@/api/client'
import {
  REDIS_PERSISTENCE,
  REDIS_POLICIES,
  configureRedis,
  revealRedisPassword,
  type DbEngine,
  type RedisConfigRequest,
  type RedisPersistence,
} from '@/api/databases'
import Card from '@/components/common/Card.vue'
import FormField from '@/components/common/FormField.vue'
import Notice from '@/components/common/Notice.vue'
import OptionGroup from '@/components/common/OptionGroup.vue'
import PasswordInput from '@/components/common/PasswordInput.vue'
import SelectField from '@/components/common/SelectField.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import type { OptionItem, SelectOption } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { isDbPassword } from '@/databases/validate'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { notifyError } from '@/lib/notify'
import { useDatabasesStore } from '@/stores/databases'
import PasswordDialog from './PasswordDialog.vue'

// The system Redis/Valkey instance's basic settings: password, memory limit
// and eviction, persistence. Saving asks for the password (step-up) and
// runs as a job.
const props = defineProps<{ engine: DbEngine; canManage: boolean }>()

const { t } = useI18n()
const store = useDatabasesStore()

type PasswordMode = 'keep' | 'set' | 'generate' | 'remove'

const form = reactive({
  passwordMode: 'keep' as string,
  password: '',
  maxMemoryMb: '0',
  policy: 'noeviction',
  persistence: 'rdb' as string,
})
const errors = ref<{ password?: string; maxMemoryMb?: string }>({})
const saving = ref(false)
const error = ref<unknown>(null)
const revealed = ref<string | null>(null)

function reset() {
  const redis = props.engine.redis
  Object.assign(form, {
    passwordMode: 'keep',
    password: '',
    maxMemoryMb: String(redis?.maxMemoryMb ?? 0),
    policy: redis?.maxMemoryPolicy ?? 'noeviction',
    persistence: redis?.persistence ?? 'rdb',
  })
  errors.value = {}
}

// The list reloads whenever a job starts or ends; edits stay unless the settings themselves changed.
watch(() => `${props.engine.id}:${JSON.stringify(props.engine.redis ?? null)}`, reset, { immediate: true })

const passwordSet = computed(() => props.engine.redis?.passwordSet === true)

const modeOptions = computed<OptionItem[]>(() => {
  const list: OptionItem[] = [
    { value: 'keep', label: t('databases.redis.mode.keep') },
    { value: 'generate', label: t('databases.redis.generate') },
    { value: 'set', label: t('databases.redis.setOwn') },
  ]
  if (passwordSet.value) list.push({ value: 'remove', label: t('databases.redis.mode.remove') })
  return list
})
const policyOptions: SelectOption[] = REDIS_POLICIES.map((value) => ({ value, label: value }))
const persistenceOptions = computed<SelectOption[]>(() =>
  REDIS_PERSISTENCE.map((value) => ({ value, label: t(`databases.redis.persistence.${value}`) })),
)

const memory = computed(() => Number(form.maxMemoryMb))

async function reveal() {
  try {
    revealed.value = await revealRedisPassword()
  } catch (err) {
    if (!isStepUpRequired(err)) notifyError(err)
  }
}

async function save() {
  const next: { password?: string; maxMemoryMb?: string } = {}
  if (form.passwordMode === 'set' && !isDbPassword(form.password)) next.password = validationMessage('password', 'db_password')
  if (!Number.isInteger(memory.value) || memory.value < 0) next.maxMemoryMb = validationMessage('maxMemoryMb', 'range', { min: 0 })
  errors.value = next
  if (Object.keys(next).length > 0) return
  const redis = props.engine.redis
  const request: RedisConfigRequest = {}
  const mode = form.passwordMode as PasswordMode
  if (mode === 'set') request.password = form.password
  if (mode === 'generate') request.generatePassword = true
  if (mode === 'remove') request.password = ''
  if (memory.value !== (redis?.maxMemoryMb ?? 0)) request.maxMemoryMb = memory.value
  if (form.policy !== redis?.maxMemoryPolicy) request.maxMemoryPolicy = form.policy
  if (form.persistence !== redis?.persistence) request.persistence = form.persistence as RedisPersistence
  saving.value = true
  error.value = null
  try {
    // Step-up protected: the client asks for the password and retries.
    await store.runJob(() => configureRedis(request), { rethrow: true })
    // The password change is on its way; saving again would repeat it.
    form.passwordMode = 'keep'
    form.password = ''
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <Card :title="t('databases.tabs.settings')" :level="3">
    <form class="flex max-w-[560px] flex-col gap-5" novalidate @submit.prevent="save">
      <fieldset class="m-0 flex min-w-0 flex-col gap-2 border-0 p-0" :disabled="!canManage || saving">
        <legend class="mb-1.5 flex flex-wrap items-center gap-2 p-0 text-detail font-medium">
          {{ t('common.field.password') }}
          <StatusPill :tone="passwordSet ? 'success' : 'warning'">
            {{ passwordSet ? t('databases.redis.passwordOn') : t('databases.redis.passwordOff') }}
          </StatusPill>
        </legend>
        <Button
          v-if="canManage && engine.redisPasswordKnown"
          type="button"
          variant="outline"
          size="sm"
          class="self-start"
          @click="reveal"
        >
          <Eye aria-hidden="true" />
          {{ t('databases.password.show') }}
        </Button>
        <OptionGroup v-model="form.passwordMode" :options="modeOptions" grid :label="t('common.field.password')" :disabled="!canManage || saving" />
        <FormField v-if="form.passwordMode === 'set'" id="redis-password" :label="t('databases.redis.newPassword')" :hint="t('databases.hints.password')" :error="errors.password">
          <template #default="field">
            <PasswordInput v-bind="field" v-model="form.password" autocomplete="new-password" />
          </template>
        </FormField>
        <Notice v-if="form.passwordMode === 'remove' && engine.remote" tone="warning">{{ t('databases.redis.removeRemote') }}</Notice>
      </fieldset>

      <FormField id="redis-memory" :label="t('common.field.maxMemoryMb')" :hint="t('databases.redis.maxMemoryHint')" :error="errors.maxMemoryMb">
        <template #default="field">
          <Input v-bind="field" v-model="form.maxMemoryMb" type="number" min="0" step="1" inputmode="numeric" class="max-w-40 tabular-nums" :disabled="!canManage || saving" />
        </template>
      </FormField>
      <FormField id="redis-policy" :label="t('common.field.maxMemoryPolicy')">
        <template #default="field">
          <SelectField v-bind="field" v-model="form.policy" :options="policyOptions" class="max-w-64 font-mono" :disabled="!canManage || saving" />
        </template>
      </FormField>
      <FormField id="redis-persistence" :label="t('common.field.persistence')" :hint="t('databases.redis.persistenceHint')">
        <template #default="field">
          <SelectField v-bind="field" v-model="form.persistence" :options="persistenceOptions" class="max-w-80" :disabled="!canManage || saving" />
        </template>
      </FormField>

      <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
      <div v-if="canManage" class="flex flex-wrap gap-2">
        <Button type="submit" class="max-md:h-11" :disabled="saving">
          <LoaderCircle v-if="saving" class="animate-spin" aria-hidden="true" />
          {{ t('databases.redis.save') }}
        </Button>
        <Button type="button" variant="ghost" class="max-md:h-11" :disabled="saving" @click="reset">{{ t('databases.redis.discard') }}</Button>
      </div>
    </form>
  </Card>
  <PasswordDialog v-model:password="revealed" :title="t('databases.redis.passwordTitle')" />
</template>
