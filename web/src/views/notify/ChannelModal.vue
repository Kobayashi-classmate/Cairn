<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { I18nT, useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NCheckbox,
  NCheckboxGroup,
  NDynamicTags,
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
} from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import {
  CHANNEL_TYPES,
  EMAIL_SECURITY,
  EVENT_CATEGORIES,
  LEVELS,
  createChannel,
  updateChannel,
  type Channel,
  type ChannelInput,
  type ChannelType,
  type EmailSecurity,
  type Level,
} from '@/api/notify'
import SecretInput from '@/components/SecretInput.vue'
import {
  PORT_MAX,
  PORT_MIN,
  checkRule,
  isEmail,
  isHttpUrl,
  listRule,
  rangeRule,
  requiredListRule,
  requiredRule,
} from '@/forms/rules'
import { secretPatch, type SecretMode } from '@/forms/secret'
import { SOURCE_LOCALE, localeNativeName } from '@/i18n'
import { errorMessage } from '@/i18n/errors'
import { useSessionStore } from '@/stores/session'

const show = defineModel<boolean>('show', { required: true })
/** The channel to edit, or null to create one. */
const props = defineProps<{ channel: Channel | null }>()
const emit = defineEmits<{ saved: [channel: Channel] }>()

const { t } = useI18n()
const session = useSessionStore()

const DEFAULT_PORT: Record<EmailSecurity, number> = { starttls: 587, tls: 465, none: 25 }

/**
 * Example webhook address for the empty field (not text to translate). Kept
 * out of the template: a literal URL attribute there trips vue-tsc.
 */
const WEBHOOK_EXAMPLE = 'https://example.com/hooks/cairn'

function emptyForm() {
  return {
    type: 'email' as ChannelType,
    name: '',
    locale: session.meta?.defaultLocale ?? SOURCE_LOCALE,
    enabled: true,
    events: [...EVENT_CATEGORIES] as string[],
    minLevel: 'warning' as Level,
    email: {
      host: '',
      port: DEFAULT_PORT.starttls as number | null,
      security: 'starttls' as EmailSecurity,
      username: '',
      from: '',
      to: [] as string[],
    },
    webhook: { url: '' },
  }
}

const formRef = ref<FormInst | null>(null)
const form = reactive(emptyForm())
const secret = reactive({ mode: 'keep' as SecretMode, value: '' })
const submitting = ref(false)
const error = ref<unknown>(null)

const editing = computed(() => props.channel !== null)

/** Whether the server holds a password (email) or signing secret (webhook). */
const secretStored = computed(() => {
  const channel = props.channel
  if (!channel || channel.type !== form.type) return false
  return channel.type === 'email' ? channel.config.hasPassword : channel.config.hasSecret
})

watch(show, (open) => {
  if (!open) return
  Object.assign(form, emptyForm())
  secret.mode = 'keep'
  secret.value = ''
  error.value = null
  const channel = props.channel
  if (!channel) return
  Object.assign(form, {
    type: channel.type,
    name: channel.name,
    locale: channel.locale,
    enabled: channel.enabled,
    events: [...channel.events],
    minLevel: channel.minLevel,
  })
  if (channel.type === 'email') {
    const { host, port, security, username, from, to } = channel.config
    Object.assign(form.email, { host, port, security, username, from, to: [...to] })
  } else {
    form.webhook.url = channel.config.url
  }
})

// Keep the port in step with the encryption mode while it is still a default.
watch(
  () => form.email.security,
  (security, previous) => {
    if (form.email.port === null || form.email.port === DEFAULT_PORT[previous]) {
      form.email.port = DEFAULT_PORT[security]
    }
  },
)

const rules = computed<FormRules>(() => {
  const common: FormRules = {
    name: [requiredRule('name')],
    locale: [requiredRule('locale')],
    events: [requiredListRule('events')],
  }
  if (form.type === 'email') {
    common.email = {
      host: [requiredRule('host')],
      port: [rangeRule('port', PORT_MIN, PORT_MAX)],
      from: [requiredRule('from'), checkRule('from', 'email', isEmail)],
      to: [requiredListRule('to'), listRule('to', 'email', isEmail)],
    }
  } else {
    common.webhook = { url: [requiredRule('url'), checkRule('url', 'url', isHttpUrl)] }
  }
  return common
})

const typeOptions = computed(() =>
  CHANNEL_TYPES.map((type) => ({ value: type, label: t(`notify.type.${type}`) })),
)
const levelOptions = computed(() =>
  LEVELS.map((level) => ({ value: level, label: t(`notify.level.${level}`) })),
)
const securityOptions = computed(() =>
  EMAIL_SECURITY.map((value) => ({ value, label: t(`notify.email.securityMode.${value}`) })),
)
// Channel messages may use any language the server offers.
const localeOptions = computed(() =>
  (session.meta?.locales ?? [SOURCE_LOCALE]).map((code) => ({
    value: code,
    label: localeNativeName(code),
  })),
)

/** "security." → "security", the key used for its labels. */
function categoryKey(prefix: string): string {
  return prefix.replace(/\.$/, '')
}

function buildInput(): ChannelInput {
  const patch = secretPatch(secret.mode, secret.value)
  const config =
    form.type === 'email'
      ? {
          host: form.email.host.trim(),
          port: form.email.port ?? DEFAULT_PORT[form.email.security],
          security: form.email.security,
          username: form.email.username,
          from: form.email.from.trim(),
          to: form.email.to.map((address) => address.trim()),
          ...(patch !== undefined ? { password: patch } : {}),
        }
      : {
          url: form.webhook.url.trim(),
          ...(patch !== undefined ? { secret: patch } : {}),
        }
  return {
    type: form.type,
    name: form.name.trim(),
    locale: form.locale,
    enabled: form.enabled,
    events: form.events,
    minLevel: form.minLevel,
    config,
  }
}

async function submit() {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  error.value = null
  submitting.value = true
  try {
    const input = buildInput()
    let saved: Channel
    if (props.channel) {
      // The type cannot change; everything else is sent as a whole.
      const { type: _type, ...changes } = input
      saved = await updateChannel(props.channel.id, changes)
    } else {
      saved = await createChannel(input)
    }
    emit('saved', saved)
    show.value = false
  } catch (err) {
    if (!isStepUpRequired(err)) error.value = err
  } finally {
    submitting.value = false
  }
}

function onTypeChange() {
  secret.mode = 'keep'
  secret.value = ''
  formRef.value?.restoreValidation()
}
</script>

<template>
  <NModal
    v-model:show="show"
    preset="card"
    :title="editing ? t('notify.channel.editTitle') : t('notify.channel.createTitle')"
    class="channel-modal"
  >
    <NForm ref="formRef" :model="form" :rules="rules" :disabled="submitting" @submit.prevent="submit">
      <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>

      <NFormItem path="type" :label="t('common.field.type')">
        <NRadioGroup v-model:value="form.type" :disabled="editing" @update:value="onTypeChange">
          <NRadioButton v-for="option in typeOptions" :key="option.value" :value="option.value">
            {{ option.label }}
          </NRadioButton>
        </NRadioGroup>
      </NFormItem>

      <div class="row">
        <NFormItem path="name" :label="t('common.field.name')" class="row__wide">
          <NInput v-model:value="form.name" :input-props="{ autocomplete: 'off' }" />
        </NFormItem>
        <NFormItem :label="t('notify.channel.enabled')">
          <NSwitch v-model:value="form.enabled" />
        </NFormItem>
      </div>

      <NFormItem path="events" :label="t('common.field.events')">
        <NCheckboxGroup v-model:value="form.events">
          <div class="events">
            <NCheckbox v-for="prefix in EVENT_CATEGORIES" :key="prefix" :value="prefix">
              <div>{{ t(`notify.category.${categoryKey(prefix)}.label`) }}</div>
              <NText depth="3" class="hint">
                {{ t(`notify.category.${categoryKey(prefix)}.description`) }}
              </NText>
            </NCheckbox>
          </div>
        </NCheckboxGroup>
      </NFormItem>

      <div class="row">
        <NFormItem path="minLevel" :label="t('common.field.minLevel')" class="row__half">
          <NSelect v-model:value="form.minLevel" :options="levelOptions" />
        </NFormItem>
        <NFormItem path="locale" :label="t('notify.channel.locale')" class="row__half">
          <NSelect v-model:value="form.locale" :options="localeOptions" />
        </NFormItem>
      </div>

      <template v-if="form.type === 'email'">
        <div class="row">
          <NFormItem path="email.host" :label="t('common.field.host')" class="row__wide">
            <NInput
              v-model:value="form.email.host"
              :input-props="{ autocomplete: 'off', spellcheck: false }"
              placeholder="smtp.example.com"
            />
          </NFormItem>
          <NFormItem path="email.port" :label="t('common.field.port')" class="row__narrow">
            <NInputNumber
              v-model:value="form.email.port"
              :min="PORT_MIN"
              :max="PORT_MAX"
              :show-button="false"
            />
          </NFormItem>
        </div>
        <NFormItem path="email.security" :label="t('common.field.security')">
          <NSelect v-model:value="form.email.security" :options="securityOptions" />
        </NFormItem>
        <div class="row">
          <NFormItem path="email.username" :label="t('common.field.username')" class="row__half">
            <NInput
              v-model:value="form.email.username"
              :input-props="{ autocomplete: 'off', spellcheck: false }"
            />
          </NFormItem>
          <NFormItem :label="t('common.field.password')" class="row__half">
            <SecretInput
              v-model:mode="secret.mode"
              v-model:value="secret.value"
              :stored="secretStored"
            />
          </NFormItem>
        </div>
        <NFormItem path="email.from" :label="t('common.field.from')">
          <NInput
            v-model:value="form.email.from"
            :input-props="{ autocomplete: 'off', spellcheck: false, type: 'email' }"
            placeholder="cairn@example.com"
          />
        </NFormItem>
        <NFormItem path="email.to" :label="t('common.field.to')">
          <NDynamicTags v-model:value="form.email.to" />
        </NFormItem>
      </template>

      <template v-else>
        <NFormItem path="webhook.url" :label="t('common.field.url')">
          <NInput
            v-model:value="form.webhook.url"
            :input-props="{ autocomplete: 'off', spellcheck: false, type: 'url' }"
            :placeholder="WEBHOOK_EXAMPLE"
          />
        </NFormItem>
        <NFormItem :label="t('common.field.secret')">
          <SecretInput
            v-model:mode="secret.mode"
            v-model:value="secret.value"
            :stored="secretStored"
          />
        </NFormItem>
        <NAlert type="info" :show-icon="false" class="block">
          <p class="help">{{ t('notify.webhook.payload') }}</p>
          <I18nT keypath="notify.webhook.signature" tag="p" class="help">
            <template #signatureHeader>
              <code dir="ltr">X-Cairn-Signature: sha256=&lt;hex&gt;</code>
            </template>
            <template #signedContent>
              <code dir="ltr">&lt;X-Cairn-Timestamp&gt;.&lt;body&gt;</code>
            </template>
          </I18nT>
        </NAlert>
      </template>

      <div class="actions">
        <NButton :disabled="submitting" @click="show = false">{{ t('common.action.cancel') }}</NButton>
        <NButton type="primary" attr-type="submit" :loading="submitting">
          {{ t('common.action.save') }}
        </NButton>
      </div>
    </NForm>
  </NModal>
</template>

<style scoped>
.channel-modal {
  inline-size: min(640px, calc(100vw - 32px));
}

.block {
  margin-block-end: 16px;
}

.row {
  display: flex;
  flex-wrap: wrap;
  gap: 0 16px;
}

.row__wide {
  flex: 3 1 240px;
}

.row__half {
  flex: 1 1 220px;
}

.row__narrow {
  flex: 1 1 100px;
}

.events {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.hint {
  font-size: 13px;
}

.help {
  margin: 0 0 4px;
}

.help code {
  overflow-wrap: anywhere;
}

.actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
