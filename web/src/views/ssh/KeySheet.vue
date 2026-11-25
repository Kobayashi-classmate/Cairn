<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { isApiError, isStepUpRequired } from '@/api/client'
import { addSshKey } from '@/api/ssh'
import FormField from '@/components/common/FormField.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import { Textarea } from '@/components/ui/textarea'
import { errorMessage } from '@/i18n/errors'
import { notifySuccess } from '@/lib/notify'

// Authorizes a public key for one user (step-up protected).
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{ user: string }>()
const emit = defineEmits<{ added: [] }>()

/**
 * One public key of a type the server accepts, without options in front:
 * ssh-ed25519, ssh-rsa, ecdsa-sha2-nistp256/384/521 and the FIDO (sk-) keys.
 * DSA keys and option prefixes are refused by the server, so not offered here.
 */
const KEY_PATTERN =
  /^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521)|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com)\s+[A-Za-z0-9+/=]+(\s.*)?$/

const { t } = useI18n()
const key = ref('')
const fieldError = ref<string | null>(null)
const submitting = ref(false)
const error = ref<unknown>(null)

watch(open, (value) => {
  if (!value) return
  key.value = ''
  fieldError.value = null
  error.value = null
})

const file = computed(() => (props.user === 'root' ? '/root/.ssh/authorized_keys' : `~${props.user}/.ssh/authorized_keys`))
const steps = computed(() => [t('ssh.keys.steps.add', { file: file.value }), t('ssh.keys.steps.mode')])

async function submit() {
  const value = key.value.trim()
  if (!KEY_PATTERN.test(value)) {
    fieldError.value = t('errors.ssh.key_invalid')
    return
  }
  fieldError.value = null
  submitting.value = true
  error.value = null
  try {
    // Step-up protected: the client asks for the password and retries.
    await addSshKey(props.user, value)
    notifySuccess(t('ssh.keys.added'))
    emit('added')
    open.value = false
  } catch (err) {
    if (isStepUpRequired(err)) return
    if (isApiError(err) && (err.code === 'ssh.key_invalid' || err.code === 'ssh.key_exists')) fieldError.value = errorMessage(err)
    else error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <FormSheet
    v-model:open="open"
    :title="t('ssh.keys.addTitle', { user })"
    :description="t('ssh.keys.addIntro')"
    :submit-label="t('ssh.keys.add')"
    :submitting="submitting"
    :steps="steps"
    @submit="submit"
  >
    <FormField id="ssh-key" :label="t('ssh.keys.key')" :hint="t('ssh.keys.keyHint')" :error="fieldError">
      <template #default="field">
        <Textarea
          v-bind="field"
          v-model="key"
          rows="5"
          spellcheck="false"
          autocomplete="off"
          dir="ltr"
          class="max-h-60 font-mono text-xs [overflow-wrap:anywhere]"
          :placeholder="t('ssh.keys.placeholder')"
        />
      </template>
    </FormField>
    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
