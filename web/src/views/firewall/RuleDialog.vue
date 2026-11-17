<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Adds or changes a port rule. Rules opened by another feature only let
// their sources change.
import { computed, nextTick, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NDynamicTags,
  NForm,
  NFormItem,
  NInput,
  NModal,
  NRadioButton,
  NRadioGroup,
  NText,
  type FormInst,
  type FormRules,
} from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import {
  NOTE_MAX,
  PORTS_MAX,
  RULE_PROTOCOLS,
  SOURCES_MAX,
  addRule,
  isPortSpec,
  updateRule,
  type ChangeResult,
  type FirewallRule,
  type RuleAction,
  type RuleProtocol,
} from '@/api/firewall'
import { checkRule, isIpOrCidr, listRule } from '@/forms/rules'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { ownerLabel } from './owners'

const show = defineModel<boolean>('show', { required: true })
const props = defineProps<{
  /** The rule to change, or null for a new one. */
  rule: FirewallRule | null
  /** The address the panel sees for this browser. */
  clientIp: string
}>()
const emit = defineEmits<{ changed: [result: ChangeResult] }>()

const { t } = useI18n()
const formRef = ref<FormInst | null>(null)
const form = reactive({
  action: 'allow' as RuleAction,
  protocol: 'tcp' as RuleProtocol,
  ports: [] as string[],
  sources: [] as string[],
  note: '',
})
const submitting = ref(false)
const error = ref<unknown>(null)

const editing = computed(() => props.rule !== null)
/** Opened by another feature: only the sources can change. */
const managed = computed(() => !!props.rule?.owner)

watch(show, async (open) => {
  if (!open) return
  const rule = props.rule
  Object.assign(form, {
    action: rule?.action ?? 'allow',
    protocol: rule?.protocol ?? 'tcp',
    ports: [...(rule?.ports ?? [])],
    sources: [...(rule?.sources ?? [])],
    note: rule?.note ?? '',
  })
  error.value = null
  await nextTick()
  formRef.value?.restoreValidation()
})

const rules = computed<FormRules>(() => ({
  ports: [
    {
      trigger: 'change',
      validator: (_rule, value: string[]) => value.length > 0,
      renderMessage: () => validationMessage('ports', 'required'),
    },
    listRule('ports', 'port_spec', isPortSpec),
    {
      trigger: 'change',
      validator: (_rule, value: string[]) => value.length <= PORTS_MAX,
      renderMessage: () => validationMessage('ports', 'count'),
    },
  ],
  sources: [
    listRule('sources', 'ip', isIpOrCidr),
    {
      trigger: 'change',
      validator: (_rule, value: string[]) => value.length <= SOURCES_MAX,
      renderMessage: () => validationMessage('sources', 'count'),
    },
    {
      // Inbound traffic is denied by default: a deny rule only makes sense for some sources.
      trigger: 'change',
      validator: (_rule, value: string[]) => form.action !== 'deny' || value.length > 0,
      renderMessage: () => t('errors.firewall.deny_needs_source'),
    },
  ],
  note: [checkRule('note', 'max_length', (v) => [...v].length <= NOTE_MAX, { max: NOTE_MAX })],
}))

function addMine() {
  if (props.clientIp && !form.sources.includes(props.clientIp)) form.sources = [...form.sources, props.clientIp]
}

async function submit() {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  const body = {
    action: form.action,
    protocol: form.protocol,
    ports: form.ports.map((port) => port.trim()),
    sources: form.sources.map((source) => source.trim()),
    note: form.note.trim(),
  }
  submitting.value = true
  error.value = null
  try {
    // Step-up protected: the client asks for the password and retries.
    const result = props.rule ? await updateRule(props.rule.id, body) : await addRule(body)
    emit('changed', result)
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
    :title="editing ? t('firewall.rule.editTitle') : t('firewall.rule.createTitle')"
    class="rule-dialog"
    :mask-closable="false"
  >
    <NAlert v-if="managed && rule" type="info" class="block">{{ t('firewall.rule.managedNote', { owner: ownerLabel(rule.owner) }) }}</NAlert>
    <NForm ref="formRef" :model="form" :rules="rules" :disabled="submitting" label-placement="top">
      <div class="row">
        <NFormItem :label="t('common.field.action')" :show-feedback="false">
          <NRadioGroup v-model:value="form.action" :disabled="managed">
            <NRadioButton value="allow">{{ t('firewall.action.allow') }}</NRadioButton>
            <NRadioButton value="deny">{{ t('firewall.action.deny') }}</NRadioButton>
          </NRadioGroup>
        </NFormItem>
        <NFormItem :label="t('common.field.protocol')" :show-feedback="false">
          <NRadioGroup v-model:value="form.protocol" :disabled="managed">
            <NRadioButton v-for="protocol in RULE_PROTOCOLS" :key="protocol" :value="protocol">
              {{ t(`firewall.protocol.${protocol}`) }}
            </NRadioButton>
          </NRadioGroup>
        </NFormItem>
      </div>
      <NFormItem path="ports" :label="t('common.field.ports')" class="gap">
        <div class="field">
          <NDynamicTags v-model:value="form.ports" :disabled="managed" :max="PORTS_MAX" />
          <NText depth="3" class="hint">{{ t('firewall.rule.portsHint', { max: PORTS_MAX }) }}</NText>
        </div>
      </NFormItem>
      <NFormItem path="sources" :label="t('common.field.sources')">
        <div class="field">
          <NDynamicTags v-model:value="form.sources" :max="SOURCES_MAX" />
          <div class="sources__extra">
            <NText depth="3" class="hint">
              {{ form.action === 'deny' ? t('firewall.rule.denyHint') : t('firewall.rule.sourcesHint') }}
            </NText>
            <NButton v-if="clientIp" size="tiny" quaternary @click="addMine">{{ t('firewall.rule.addMine', { ip: clientIp }) }}</NButton>
          </div>
        </div>
      </NFormItem>
      <NFormItem path="note" :label="t('common.field.note')">
        <NInput v-model:value="form.note" :maxlength="NOTE_MAX" :disabled="managed" />
      </NFormItem>
    </NForm>
    <NText depth="3" tag="p" class="hint">{{ t('firewall.confirm.explain') }}</NText>
    <NAlert v-if="error" type="error" class="block">{{ errorMessage(error) }}</NAlert>
    <div class="actions">
      <NButton :disabled="submitting" @click="show = false">{{ t('common.action.cancel') }}</NButton>
      <NButton type="primary" :loading="submitting" @click="submit">{{ t('common.action.save') }}</NButton>
    </div>
  </NModal>
</template>

<style scoped>
.rule-dialog {
  inline-size: min(560px, calc(100vw - 32px));
}

.row {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 24px;
  margin-block-end: 16px;
}

.gap {
  margin-block-start: 0;
}

.field {
  display: flex;
  flex-direction: column;
  gap: 6px;
  inline-size: 100%;
}

.sources__extra {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 4px 8px;
}

.hint {
  font-size: 12px;
}

.block {
  margin-block: 8px 12px;
}

.actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-block-start: 12px;
}
</style>
