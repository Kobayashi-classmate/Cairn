<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NButton,
  NForm,
  NFormItem,
  NInput,
  NInputNumber,
  NRadio,
  NRadioGroup,
  NSelect,
  NTag,
  NText,
} from 'naive-ui'
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
import type { Job } from '@/api/jobs'
import { useErrorToast } from '@/composables/useErrorToast'
import { isDbPassword } from '@/databases/validate'
import { validationMessage } from '@/i18n/errors'
import PasswordDialog from './PasswordDialog.vue'

const props = defineProps<{ engine: DbEngine; canManage: boolean }>()
const emit = defineEmits<{ started: [job: Job] }>()

const { t } = useI18n()
const showError = useErrorToast()

type PasswordMode = 'keep' | 'set' | 'generate' | 'remove'

const form = reactive({
  passwordMode: 'keep' as PasswordMode,
  password: '',
  maxMemoryMb: 0 as number | null,
  policy: 'noeviction',
  persistence: 'rdb' as RedisPersistence,
})
const saving = ref(false)
const passwordError = ref('')
const revealed = ref<string | null>(null)

function reset() {
  const redis = props.engine.redis
  Object.assign(form, {
    passwordMode: 'keep',
    password: '',
    maxMemoryMb: redis?.maxMemoryMb ?? 0,
    policy: redis?.maxMemoryPolicy ?? 'noeviction',
    persistence: redis?.persistence ?? 'rdb',
  })
  passwordError.value = ''
}

watch(() => props.engine, reset, { immediate: true })

const passwordSet = computed(() => props.engine.redis?.passwordSet === true)
const policyOptions = REDIS_POLICIES.map((value) => ({ value, label: value }))
const persistenceOptions = computed(() =>
  REDIS_PERSISTENCE.map((value) => ({ value, label: t(`databases.redis.persistence.${value}`) })),
)

async function reveal() {
  try {
    revealed.value = await revealRedisPassword()
  } catch (err) {
    if (!isStepUpRequired(err)) showError(err)
  }
}

async function save() {
  passwordError.value = ''
  if (form.passwordMode === 'set' && !isDbPassword(form.password)) {
    passwordError.value = validationMessage('password', 'db_password')
    return
  }
  const redis = props.engine.redis
  const request: RedisConfigRequest = {}
  if (form.passwordMode === 'set') request.password = form.password
  if (form.passwordMode === 'generate') request.generatePassword = true
  if (form.passwordMode === 'remove') request.password = ''
  if ((form.maxMemoryMb ?? 0) !== (redis?.maxMemoryMb ?? 0)) request.maxMemoryMb = form.maxMemoryMb ?? 0
  if (form.policy !== redis?.maxMemoryPolicy) request.maxMemoryPolicy = form.policy
  if (form.persistence !== redis?.persistence) request.persistence = form.persistence
  saving.value = true
  try {
    // Step-up protected: the client asks for the password and retries.
    emit('started', await configureRedis(request))
  } catch (err) {
    showError(err)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="settings">
    <NForm :disabled="!canManage || saving" :show-feedback="false">
      <NFormItem :label="t('common.field.password')" class="item">
        <div class="field">
          <div class="row">
            <NTag :type="passwordSet ? 'success' : 'warning'" :bordered="false">
              {{ passwordSet ? t('databases.redis.passwordOn') : t('databases.redis.passwordOff') }}
            </NTag>
            <NButton v-if="canManage && engine.redisPasswordKnown" size="small" @click="reveal">
              {{ t('databases.password.show') }}
            </NButton>
          </div>
          <NRadioGroup v-model:value="form.passwordMode" class="modes">
            <NRadio value="keep">{{ t('common.secret.keep') }}</NRadio>
            <NRadio value="generate">{{ t('databases.redis.generate') }}</NRadio>
            <NRadio value="set">{{ t('databases.redis.setOwn') }}</NRadio>
            <NRadio v-if="passwordSet" value="remove">{{ t('common.secret.clear') }}</NRadio>
          </NRadioGroup>
          <NInput
            v-if="form.passwordMode === 'set'"
            v-model:value="form.password"
            type="password"
            show-password-on="click"
            :input-props="{ autocomplete: 'new-password' }"
          />
          <NText v-if="passwordError" type="error" class="hint">{{ passwordError }}</NText>
          <NText v-if="form.passwordMode === 'remove' && engine.remote" type="warning" class="hint">
            {{ t('databases.redis.removeRemote') }}
          </NText>
        </div>
      </NFormItem>
      <NFormItem :label="t('common.field.maxMemoryMb')" class="item">
        <div class="field">
          <NInputNumber v-model:value="form.maxMemoryMb" :min="0" :show-button="false" class="narrow" />
          <NText depth="3" class="hint">{{ t('databases.redis.maxMemoryHint') }}</NText>
        </div>
      </NFormItem>
      <NFormItem :label="t('common.field.maxMemoryPolicy')" class="item">
        <NSelect v-model:value="form.policy" :options="policyOptions" class="narrow" />
      </NFormItem>
      <NFormItem :label="t('common.field.persistence')" class="item">
        <div class="field">
          <NSelect v-model:value="form.persistence" :options="persistenceOptions" class="narrow" />
          <NText depth="3" class="hint">{{ t('databases.redis.persistenceHint') }}</NText>
        </div>
      </NFormItem>
    </NForm>
    <NButton v-if="canManage" type="primary" :loading="saving" @click="save">{{ t('common.action.save') }}</NButton>
    <PasswordDialog v-model:password="revealed" :title="t('databases.redis.passwordTitle')" />
  </div>
</template>

<style scoped>
.settings {
  max-inline-size: 560px;
}

.item {
  margin-block-end: 16px;
}

.field {
  display: flex;
  flex-direction: column;
  gap: 8px;
  inline-size: 100%;
}

.row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.modes {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 16px;
}

.narrow {
  max-inline-size: 260px;
}

.hint {
  font-size: 12px;
}
</style>
