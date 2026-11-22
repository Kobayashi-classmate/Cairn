<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { I18nT, useI18n } from 'vue-i18n'
import { LoaderCircle, Send } from '@lucide/vue'
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
  type EmailSecurity,
  type Level,
} from '@/api/notify'
import FormField from '@/components/common/FormField.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import SegmentedControl from '@/components/common/SegmentedControl.vue'
import type { SegmentedOption } from '@/components/common/segmented'
import SelectField from '@/components/common/SelectField.vue'
import SwitchField from '@/components/common/SwitchField.vue'
import TagInput from '@/components/common/TagInput.vue'
import type { SelectOption } from '@/components/common/types'
import SecretInput from '@/components/SecretInput.vue'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { PORT_MAX, PORT_MIN, isEmail, isHttpUrl } from '@/forms/rules'
import { secretPatch, type SecretMode } from '@/forms/secret'
import { SOURCE_LOCALE, localeNativeName } from '@/i18n'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { useSessionStore } from '@/stores/session'

// Adding or changing a notification channel (form sheet, re-authentication):
// email over SMTP or a webhook, the event categories it receives, the
// lowest level sent and the language of its messages. Stored passwords and
// signing secrets are never shown; they can be kept, replaced or removed.
// An existing channel can send a test message from here.
const open = defineModel<boolean>('open', { required: true })
/** The channel to edit, or null to add one. */
const props = defineProps<{
  channel: Channel | null
  /** A test message to this channel is on its way. */
  testing?: boolean
}>()
const emit = defineEmits<{ saved: [channel: Channel]; test: [channel: Channel] }>()

const { t } = useI18n()
const session = useSessionStore()

const DEFAULT_PORT: Record<EmailSecurity, number> = { starttls: 587, tls: 465, none: 25 }

/** Example addresses for empty fields (not text to translate). */
const SMTP_EXAMPLE = 'smtp.example.com'
const FROM_EXAMPLE = 'cairn@example.com'
const WEBHOOK_EXAMPLE = 'https://example.com/hooks/cairn'

type ChannelKind = (typeof CHANNEL_TYPES)[number]
type Field = 'name' | 'events' | 'host' | 'port' | 'from' | 'to' | 'url'

function emptyForm() {
  return {
    type: 'email' as ChannelKind,
    name: '',
    locale: session.meta?.defaultLocale ?? SOURCE_LOCALE,
    enabled: true,
    events: [...EVENT_CATEGORIES] as string[],
    minLevel: 'warning' as string,
    email: {
      host: '',
      port: String(DEFAULT_PORT.starttls),
      security: 'starttls' as string,
      username: '',
      from: '',
      to: [] as string[],
    },
    webhook: { url: '' },
  }
}

const form = reactive(emptyForm())
const secret = reactive({ mode: 'keep' as SecretMode, value: '' })
const errors = ref<Partial<Record<Field, string>>>({})
const submitting = ref(false)
const error = ref<unknown>(null)
const toInput = ref<InstanceType<typeof TagInput> | null>(null)

const editing = computed(() => props.channel !== null)

/** Whether the server holds a password (email) or signing secret (webhook). */
const secretStored = computed(() => {
  const channel = props.channel
  if (!channel || channel.type !== form.type) return false
  return channel.type === 'email' ? channel.config.hasPassword : channel.config.hasSecret
})

watch(open, (value) => {
  if (!value) return
  Object.assign(form, emptyForm())
  secret.mode = 'keep'
  secret.value = ''
  errors.value = {}
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
    Object.assign(form.email, { host, port: String(port), security, username, from, to: [...to] })
  } else {
    form.webhook.url = channel.config.url
  }
})

// Keep the port in step with the encryption mode while it is still a default.
watch(
  () => form.email.security,
  (security, previous) => {
    const port = form.email.port.trim()
    if (port === '' || port === String(DEFAULT_PORT[previous as EmailSecurity])) {
      form.email.port = String(DEFAULT_PORT[security as EmailSecurity])
    }
  },
)

const typeOptions = computed<SegmentedOption<ChannelKind>[]>(() =>
  CHANNEL_TYPES.map((type) => ({ value: type, label: t(`notify.type.${type}`) })),
)
const levelOptions = computed<SelectOption[]>(() =>
  LEVELS.map((level) => ({ value: level, label: t('notify.channel.levelAtLeast', { level: t(`notify.level.${level}`) }) })),
)
const securityOptions = computed<SelectOption[]>(() =>
  EMAIL_SECURITY.map((value) => ({ value, label: t(`notify.email.securityMode.${value}`) })),
)
// Channel messages may use any language the server offers.
const localeOptions = computed<SelectOption[]>(() =>
  (session.meta?.locales ?? [SOURCE_LOCALE]).map((code) => ({ value: code, label: localeNativeName(code) })),
)

/** "security." → "security", the key used for its labels. */
function categoryKey(prefix: string): string {
  return prefix.replace(/\.$/, '')
}

function toggleEvent(prefix: string, checked: boolean | 'indeterminate') {
  form.events = checked === true
    ? EVENT_CATEGORIES.filter((p) => p === prefix || form.events.includes(p))
    : form.events.filter((p) => p !== prefix)
}

function onTypeChange(type: ChannelKind) {
  if (editing.value) return
  form.type = type
  secret.mode = 'keep'
  secret.value = ''
  errors.value = {}
}

function validate(): boolean {
  toInput.value?.commit()
  const next: Partial<Record<Field, string>> = {}
  if (!form.name.trim()) next.name = validationMessage('name', 'required')
  if (form.events.length === 0) next.events = validationMessage('events', 'required')
  if (form.type === 'email') {
    if (!form.email.host.trim()) next.host = validationMessage('host', 'required')
    const port = Number(form.email.port)
    if (!/^\d+$/.test(form.email.port.trim()) || port < PORT_MIN || port > PORT_MAX) {
      next.port = validationMessage('port', 'range', { min: PORT_MIN, max: PORT_MAX })
    }
    const from = form.email.from.trim()
    if (!from) next.from = validationMessage('from', 'required')
    else if (!isEmail(from)) next.from = validationMessage('from', 'email')
    if (form.email.to.length === 0) next.to = validationMessage('to', 'required')
    else if (form.email.to.some((address) => !isEmail(address))) next.to = validationMessage('to', 'email')
  } else {
    const url = form.webhook.url.trim()
    if (!url) next.url = validationMessage('url', 'required')
    else if (!isHttpUrl(url)) next.url = validationMessage('url', 'url')
  }
  errors.value = next
  return Object.keys(next).length === 0
}

function buildInput(): ChannelInput {
  const patch = secretPatch(secret.mode, secret.value)
  const config =
    form.type === 'email'
      ? {
          host: form.email.host.trim(),
          port: Number(form.email.port),
          security: form.email.security as EmailSecurity,
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
    minLevel: form.minLevel as Level,
    config,
  }
}

async function submit() {
  if (!validate()) return
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
    :title="editing ? t('notify.channel.editTitle') : t('notify.channel.createTitle')"
    :description="t('notify.channels.description')"
    :submit-label="editing ? t('common.action.save') : t('notify.channel.createSubmit')"
    :submitting="submitting"
    @submit="submit"
  >
    <template v-if="channel" #footer-start>
      <Button
        type="button"
        variant="ghost"
        class="me-auto max-md:h-11"
        :disabled="testing || submitting"
        :title="t('notify.test.savedHint')"
        @click="emit('test', channel)"
      >
        <LoaderCircle v-if="testing" class="animate-spin" aria-hidden="true" />
        <Send v-else aria-hidden="true" />
        {{ t('notify.test.send') }}
      </Button>
    </template>

    <div class="flex flex-col gap-1.5">
      <span class="text-detail font-medium" aria-hidden="true">{{ t('common.field.type') }}</span>
      <SegmentedControl
        :model-value="form.type"
        :options="typeOptions"
        :label="t('common.field.type')"
        :disabled="editing || submitting"
        class="w-fit min-w-56"
        @update:model-value="onTypeChange"
      />
      <p v-if="editing" class="m-0 text-xs text-muted-foreground">{{ t('notify.channel.typeFixed') }}</p>
    </div>

    <FormField id="channel-name" :label="t('common.field.name')" :error="errors.name">
      <template #default="field">
        <Input v-bind="field" v-model="form.name" autocomplete="off" :disabled="submitting" dir="auto" />
      </template>
    </FormField>

    <SwitchField v-model="form.enabled" :label="t('notify.channel.enabled')" :hint="t('notify.channel.enabledHint')" :disabled="submitting" />

    <fieldset class="m-0 flex min-w-0 flex-col gap-2 border-0 p-0" :aria-describedby="errors.events ? 'channel-events-error' : undefined">
      <legend class="mb-1.5 p-0 text-detail font-medium">{{ t('common.field.events') }}</legend>
      <label v-for="prefix in EVENT_CATEGORIES" :key="prefix" class="flex cursor-pointer items-start gap-2.5 text-detail">
        <Checkbox
          class="mt-0.5"
          :model-value="form.events.includes(prefix)"
          :disabled="submitting"
          @update:model-value="(value: boolean | 'indeterminate') => toggleEvent(prefix, value)"
        />
        <span class="flex min-w-0 flex-col">
          <span class="font-medium">{{ t(`notify.category.${categoryKey(prefix)}.label`) }}</span>
          <span class="text-xs text-muted-foreground">{{ t(`notify.category.${categoryKey(prefix)}.description`) }}</span>
        </span>
      </label>
      <p v-if="errors.events" id="channel-events-error" class="m-0 text-xs font-medium text-destructive">{{ errors.events }}</p>
    </fieldset>

    <div class="grid gap-4 sm:grid-cols-2">
      <FormField id="channel-level" :label="t('common.field.minLevel')">
        <template #default="field">
          <SelectField v-bind="field" v-model="form.minLevel" :options="levelOptions" :disabled="submitting" />
        </template>
      </FormField>
      <FormField id="channel-locale" :label="t('notify.channel.locale')">
        <template #default="field">
          <SelectField v-bind="field" v-model="form.locale" :options="localeOptions" :disabled="submitting" />
        </template>
      </FormField>
    </div>

    <template v-if="form.type === 'email'">
      <div class="grid gap-4 sm:grid-cols-[minmax(0,1fr)_7rem]">
        <FormField id="channel-host" :label="t('common.field.host')" :error="errors.host">
          <template #default="field">
            <Input
              v-bind="field"
              v-model="form.email.host"
              :placeholder="SMTP_EXAMPLE"
              autocomplete="off"
              autocapitalize="none"
              spellcheck="false"
              dir="ltr"
              class="font-mono"
              :disabled="submitting"
            />
          </template>
        </FormField>
        <FormField id="channel-port" :label="t('common.field.port')" :error="errors.port">
          <template #default="field">
            <Input v-bind="field" v-model="form.email.port" inputmode="numeric" dir="ltr" class="font-mono" :disabled="submitting" />
          </template>
        </FormField>
      </div>
      <FormField id="channel-security" :label="t('common.field.security')">
        <template #default="field">
          <SelectField v-bind="field" v-model="form.email.security" :options="securityOptions" :disabled="submitting" />
        </template>
      </FormField>
      <FormField id="channel-username" :label="t('common.field.username')" :hint="t('notify.email.usernameHint')">
        <template #default="field">
          <Input
            v-bind="field"
            v-model="form.email.username"
            autocomplete="off"
            autocapitalize="none"
            spellcheck="false"
            dir="ltr"
            :disabled="submitting"
          />
        </template>
      </FormField>
      <FormField id="channel-password" :label="t('common.field.password')">
        <template #default="field">
          <SecretInput
            v-bind="field"
            v-model:mode="secret.mode"
            v-model:value="secret.value"
            :stored="secretStored"
            :label="t('common.field.password')"
            :disabled="submitting"
          />
        </template>
      </FormField>
      <FormField id="channel-from" :label="t('common.field.from')" :error="errors.from">
        <template #default="field">
          <Input
            v-bind="field"
            v-model="form.email.from"
            type="email"
            :placeholder="FROM_EXAMPLE"
            autocomplete="off"
            spellcheck="false"
            dir="ltr"
            :disabled="submitting"
          />
        </template>
      </FormField>
      <FormField id="channel-to" :label="t('common.field.to')" :hint="t('notify.email.toHint')" :error="errors.to">
        <template #default="field">
          <TagInput ref="toInput" v-bind="field" v-model="form.email.to" :valid="isEmail" :disabled="submitting" />
        </template>
      </FormField>
    </template>

    <template v-else>
      <FormField id="channel-url" :label="t('common.field.url')" :error="errors.url">
        <template #default="field">
          <Input
            v-bind="field"
            v-model="form.webhook.url"
            type="url"
            :placeholder="WEBHOOK_EXAMPLE"
            autocomplete="off"
            autocapitalize="none"
            spellcheck="false"
            dir="ltr"
            class="font-mono"
            :disabled="submitting"
          />
        </template>
      </FormField>
      <FormField id="channel-secret" :label="t('common.field.secret')">
        <template #default="field">
          <SecretInput
            v-bind="field"
            v-model:mode="secret.mode"
            v-model:value="secret.value"
            :stored="secretStored"
            :label="t('common.field.secret')"
            :disabled="submitting"
          />
        </template>
      </FormField>
      <div class="flex flex-col gap-1.5 rounded-md bg-muted px-4 py-3 text-detail text-muted-foreground">
        <p class="m-0">{{ t('notify.webhook.payload') }}</p>
        <I18nT keypath="notify.webhook.signature" tag="p" class="m-0">
          <template #signatureHeader>
            <code class="font-mono text-[12px] text-foreground [overflow-wrap:anywhere]" dir="ltr">X-Cairn-Signature: sha256=&lt;hex&gt;</code>
          </template>
          <template #signedContent>
            <code class="font-mono text-[12px] text-foreground [overflow-wrap:anywhere]" dir="ltr">&lt;X-Cairn-Timestamp&gt;.&lt;body&gt;</code>
          </template>
        </I18nT>
      </div>
    </template>

    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
