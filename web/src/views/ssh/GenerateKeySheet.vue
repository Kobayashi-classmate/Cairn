<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { isApiError, isStepUpRequired } from '@/api/client'
import {
  KEY_COMMENT_MAX,
  KEY_PASSPHRASE_MAX,
  generateSshKey,
  type GenerateSshKeyRequest,
  type GeneratedSshKey,
  type SshKeyType,
} from '@/api/ssh'
import FormField from '@/components/common/FormField.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import OptionGroup from '@/components/common/OptionGroup.vue'
import PasswordInput from '@/components/common/PasswordInput.vue'
import { Input } from '@/components/ui/input'
import { byteLength } from '@/forms/rules'
import { errorMessage, validationMessage } from '@/i18n/errors'

// Makes a key pair on the server for one user (step-up protected). The
// server authorizes the public key like an added one; the private key comes
// back once and goes straight to the parent, which shows it. The passphrase
// is forgotten as soon as the sheet closes.
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{ user: string }>()
const emit = defineEmits<{ generated: [pair: GeneratedSshKey, encrypted: boolean] }>()

/** What the server accepts in a comment: letters, marks, numbers, punctuation, symbols and the space. */
const PRINTABLE = /^[\p{L}\p{M}\p{N}\p{P}\p{S} ]*$/u
/** ssh reads a passphrase as one line: control characters cannot be typed there. */
const CONTROL = /\p{Cc}/u

type Field = 'comment' | 'passphrase' | 'confirm'

const { t } = useI18n()
// OptionGroup works with plain strings; the choices are the key types.
const form = reactive({ type: 'ed25519' as string, comment: '', passphrase: '', confirm: '' })
const errors = ref<Partial<Record<Field, string>>>({})
const submitting = ref(false)
const error = ref<unknown>(null)

function reset() {
  Object.assign(form, { type: 'ed25519', comment: '', passphrase: '', confirm: '' })
  errors.value = {}
  error.value = null
}

// Cleared on opening and on closing, so the passphrase does not stay in
// the page after it was used.
watch(open, reset)

const keyType = computed<SshKeyType>(() => (form.type === 'rsa' ? 'rsa' : 'ed25519'))

const typeOptions = computed(() => [
  {
    value: 'ed25519',
    label: t('ssh.generate.recommended', { type: t('ssh.generate.types.ed25519') }),
    hint: t('ssh.generate.ed25519Hint'),
  },
  { value: 'rsa', label: t('ssh.generate.types.rsa'), hint: t('ssh.generate.rsaHint') },
])

const file = computed(() => (props.user === 'root' ? '/root/.ssh/authorized_keys' : `~${props.user}/.ssh/authorized_keys`))
const steps = computed(() => [
  t('ssh.generate.steps.generate', { type: t(`ssh.generate.types.${keyType.value}`) }),
  t('ssh.keys.steps.add', { file: file.value }),
  t('ssh.generate.steps.once'),
])

function validate(): boolean {
  const next: typeof errors.value = {}
  const comment = form.comment.trim()
  if (byteLength(comment) > KEY_COMMENT_MAX) next.comment = validationMessage('comment', 'max_length', { max: KEY_COMMENT_MAX })
  else if (!PRINTABLE.test(comment)) next.comment = validationMessage('comment', 'printable')
  if (byteLength(form.passphrase) > KEY_PASSPHRASE_MAX) {
    next.passphrase = validationMessage('passphrase', 'max_length', { max: KEY_PASSPHRASE_MAX })
  } else if (CONTROL.test(form.passphrase)) {
    next.passphrase = validationMessage('passphrase', 'value')
  }
  if (form.confirm !== form.passphrase) next.confirm = validationMessage('passphraseConfirm', 'mismatch')
  errors.value = next
  return Object.keys(next).length === 0
}

/** Server errors about a field are shown under it. */
function placeError(err: unknown): boolean {
  if (!isApiError(err) || err.code !== 'validation.invalid') return false
  const field = err.params.field
  if (field !== 'comment' && field !== 'passphrase') return false
  errors.value = { ...errors.value, [field]: errorMessage(err) }
  return true
}

async function submit() {
  if (!validate()) return
  const body: GenerateSshKeyRequest = { user: props.user, type: keyType.value }
  const comment = form.comment.trim()
  if (comment) body.comment = comment
  if (form.passphrase) body.passphrase = form.passphrase
  submitting.value = true
  error.value = null
  try {
    // Step-up protected: the client asks for the password and retries.
    const pair = await generateSshKey(body)
    open.value = false
    emit('generated', pair, body.passphrase !== undefined)
  } catch (err) {
    if (isStepUpRequired(err)) return
    if (!placeError(err)) error.value = err
  } finally {
    delete body.passphrase
    submitting.value = false
  }
}
</script>

<template>
  <FormSheet
    v-model:open="open"
    :title="t('ssh.generate.title', { user })"
    :description="t('ssh.generate.intro')"
    :submit-label="t('ssh.generate.submit')"
    :submitting="submitting"
    :steps="steps"
    @submit="submit"
  >
    <div class="flex flex-col gap-1.5">
      <span id="ssh-generate-type-label" class="text-detail font-medium">{{ t('ssh.generate.type') }}</span>
      <OptionGroup v-model="form.type" :options="typeOptions" aria-labelledby="ssh-generate-type-label" :disabled="submitting" />
    </div>

    <FormField id="ssh-generate-comment" :label="t('ssh.keys.comment')" :hint="t('ssh.generate.commentHint')" :error="errors.comment">
      <template #default="field">
        <Input
          v-bind="field"
          v-model="form.comment"
          :maxlength="KEY_COMMENT_MAX"
          autocomplete="off"
          spellcheck="false"
          :disabled="submitting"
        />
      </template>
    </FormField>

    <FormField id="ssh-generate-passphrase" :label="t('common.field.passphrase')" :hint="t('ssh.generate.passphraseHint')" :error="errors.passphrase">
      <template #default="field">
        <PasswordInput v-bind="field" v-model="form.passphrase" autocomplete="new-password" :disabled="submitting" />
      </template>
    </FormField>
    <FormField id="ssh-generate-confirm" :label="t('common.field.passphraseConfirm')" :error="errors.confirm">
      <template #default="field">
        <PasswordInput v-bind="field" v-model="form.confirm" autocomplete="new-password" :disabled="submitting" />
      </template>
    </FormField>

    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
