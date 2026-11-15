<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NCard,
  NCheckbox,
  NForm,
  NFormItem,
  NInput,
  NSelect,
  NSpin,
  NTag,
  NText,
  useMessage,
  type FormInst,
  type FormRules,
} from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import { fetchAcmeSettings, saveAcmeSettings, type AcmeCaRequest, type AcmeSettings } from '@/api/certs'
import { caAgreementUrl, caLabel } from '@/certs/labels'
import { checkRule, isEmail } from '@/forms/rules'
import { errorMessage } from '@/i18n/errors'
import { useSessionStore } from '@/stores/session'

const { t } = useI18n()
const message = useMessage()
const session = useSessionStore()

const settings = ref<AcmeSettings | null>(null)
const loadError = ref<unknown>(null)
const saving = ref(false)
const saveError = ref<unknown>(null)
const formRef = ref<FormInst | null>(null)

/** Editable copy; per-CA EAB HMAC keys start empty (they are never returned). */
const form = reactive({
  email: '',
  defaultCa: '',
  cas: {} as Record<string, { eabKid: string; eabHmac: string; clearHmac: boolean; directory: string; rootPem: string }>,
})

const canManage = computed(() => session.can('certs.manage'))

async function load() {
  try {
    const loaded = await fetchAcmeSettings()
    settings.value = loaded
    form.email = loaded.email
    form.defaultCa = loaded.defaultCa
    form.cas = Object.fromEntries(
      loaded.cas.map((ca) => [
        ca.name,
        { eabKid: ca.eabKid ?? '', eabHmac: '', clearHmac: false, directory: ca.directory ?? '', rootPem: ca.rootPem ?? '' },
      ]),
    )
    loadError.value = null
  } catch (err) {
    loadError.value = err
  }
}

onMounted(load)

const caOptions = computed(() => (settings.value?.cas ?? []).map((ca) => ({ value: ca.name, label: caLabel(ca.name) })))

const rules: FormRules = {
  email: [checkRule('email', 'email', isEmail)],
}

async function save() {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  const cas: Record<string, AcmeCaRequest> = {}
  for (const ca of settings.value?.cas ?? []) {
    const edit = form.cas[ca.name]
    if (!edit) continue
    const change: AcmeCaRequest = {}
    if (ca.needsEab) {
      change.eabKid = edit.eabKid.trim()
      // Empty keeps the stored key; "-" removes it.
      if (edit.clearHmac) change.eabHmac = '-'
      else if (edit.eabHmac) change.eabHmac = edit.eabHmac
    }
    if (ca.name === 'custom') {
      change.directory = edit.directory.trim()
      change.rootPem = edit.rootPem
      if (edit.eabKid.trim()) change.eabKid = edit.eabKid.trim()
      if (edit.clearHmac) change.eabHmac = '-'
      else if (edit.eabHmac) change.eabHmac = edit.eabHmac
    }
    if (Object.keys(change).length > 0) cas[ca.name] = change
  }
  saving.value = true
  saveError.value = null
  try {
    await saveAcmeSettings({ email: form.email.trim(), defaultCa: form.defaultCa, cas })
    message.success(t('settings.panel.saved'))
    await load()
  } catch (err) {
    if (!isStepUpRequired(err)) saveError.value = err
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div>
    <NAlert v-if="loadError" type="error" class="block">{{ errorMessage(loadError) }}</NAlert>
    <NAlert v-if="!canManage" type="info" class="block">{{ t('settings.panel.readOnly') }}</NAlert>
    <NSpin :show="!settings && !loadError">
      <NForm v-if="settings" ref="formRef" :model="form" :rules="rules" :disabled="!canManage || saving">
        <NCard size="small" class="block">
          <NText depth="3" tag="p" class="intro">{{ t('certs.acme.intro') }}</NText>
          <NFormItem path="email" :label="t('common.field.email')">
            <div class="field">
              <NInput v-model:value="form.email" :input-props="{ type: 'email', autocomplete: 'email' }" />
              <NText depth="3" class="hint">{{ t('certs.acme.emailHint') }}</NText>
            </div>
          </NFormItem>
          <NFormItem path="defaultCa" :label="t('common.field.defaultCa')">
            <NSelect v-model:value="form.defaultCa" :options="caOptions" />
          </NFormItem>
        </NCard>

        <NCard v-for="ca in settings.cas" :key="ca.name" size="small" class="block">
          <template #header>
            <span class="ca-title">
              {{ caLabel(ca.name) }}
              <NTag size="small" :bordered="false" :type="ca.ready ? 'success' : 'warning'">
                {{ ca.ready ? t('certs.acme.ready') : t('certs.acme.notReady') }}
              </NTag>
            </span>
          </template>
          <template #header-extra>
            <a v-if="caAgreementUrl(ca.name)" :href="caAgreementUrl(ca.name)!" target="_blank" rel="noopener noreferrer">
              {{ t('certs.issue.agreementLink') }}
            </a>
          </template>
          <NText v-if="ca.directory && ca.name !== 'custom'" depth="3" tag="p" class="small mono" dir="ltr">
            {{ ca.directory }}
          </NText>
          <template v-if="ca.name === 'custom' && form.cas[ca.name]">
            <NText depth="3" tag="p" class="intro">{{ t('certs.acme.customIntro') }}</NText>
            <NFormItem :label="t('common.field.ca.directory')">
              <NInput v-model:value="form.cas[ca.name]!.directory" :input-props="{ type: 'url', spellcheck: false }" />
            </NFormItem>
            <NFormItem :label="t('common.field.ca.rootPem')">
              <div class="field">
                <NInput
                  v-model:value="form.cas[ca.name]!.rootPem"
                  type="textarea"
                  :autosize="{ minRows: 3, maxRows: 8 }"
                  :input-props="{ spellcheck: false }"
                />
                <NText depth="3" class="hint">{{ t('certs.acme.rootHint') }}</NText>
              </div>
            </NFormItem>
          </template>
          <template v-if="(ca.needsEab || ca.name === 'custom') && form.cas[ca.name]">
            <NText v-if="ca.needsEab" depth="3" tag="p" class="intro">{{ t('certs.acme.eabIntro') }}</NText>
            <NFormItem :label="t('certs.acme.eabKid')">
              <NInput v-model:value="form.cas[ca.name]!.eabKid" :input-props="{ autocomplete: 'off', spellcheck: false }" />
            </NFormItem>
            <NFormItem :label="t('certs.acme.eabHmac')">
              <div class="field">
                <NInput
                  v-model:value="form.cas[ca.name]!.eabHmac"
                  type="password"
                  show-password-on="click"
                  :disabled="form.cas[ca.name]!.clearHmac"
                  :placeholder="ca.eabHmacSet ? t('dns.dialog.secretStored') : ''"
                  :input-props="{ autocomplete: 'new-password' }"
                />
                <NCheckbox v-if="ca.eabHmacSet" v-model:checked="form.cas[ca.name]!.clearHmac">
                  {{ t('certs.acme.clearHmac') }}
                </NCheckbox>
              </div>
            </NFormItem>
          </template>
        </NCard>

        <NAlert v-if="saveError" type="error" class="block">{{ errorMessage(saveError) }}</NAlert>
        <NButton v-if="canManage" type="primary" :loading="saving" @click="save">{{ t('common.action.save') }}</NButton>
      </NForm>
    </NSpin>
  </div>
</template>

<style scoped>
.block {
  margin-block-end: 12px;
}

.intro {
  margin-block: 0 12px;
  font-size: 13px;
}

.field {
  display: flex;
  flex-direction: column;
  gap: 6px;
  inline-size: 100%;
}

.hint,
.small {
  font-size: 12px;
}

.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}

.ca-title {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}
</style>
