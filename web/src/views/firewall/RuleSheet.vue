<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { UserRound } from '@lucide/vue'
import { isApiError, isStepUpRequired } from '@/api/client'
import {
  NOTE_MAX,
  PORTS_MAX,
  RULE_PROTOCOLS,
  SOURCES_MAX,
  addRule,
  deleteRule,
  isPortSpec,
  updateRule,
  type ChangeResult,
  type FirewallRule,
  type RuleAction,
  type RuleProtocol,
  type RuleRequest,
} from '@/api/firewall'
import FormField from '@/components/common/FormField.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import SegmentedControl from '@/components/common/SegmentedControl.vue'
import TagInput from '@/components/common/TagInput.vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import type { ChangeInfo } from '@/composables/useFirewallConfirm'
import { isIpOrCidr, textLength } from '@/forms/rules'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { describeRule, protocolLabel, ruleRequest } from './labels'
import { ownerLabel } from './owners'

// Adds or changes a port rule. A rule opened by another feature (a stream
// proxy, a database's remote access) only lets its sources change.
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{
  /** The rule to change, or null for a new one. */
  rule: FirewallRule | null
  /** The address the panel sees for this browser. */
  clientIp: string
}>()
const emit = defineEmits<{ changed: [result: ChangeResult, info: ChangeInfo] }>()

type Field = 'ports' | 'sources' | 'note'

const { t } = useI18n()
const form = reactive({
  action: 'allow' as RuleAction,
  protocol: 'tcp' as RuleProtocol,
  ports: [] as string[],
  sources: [] as string[],
  note: '',
})
const errors = ref<Partial<Record<Field, string>>>({})
const submitting = ref(false)
const error = ref<unknown>(null)
const portsInput = ref<InstanceType<typeof TagInput> | null>(null)
const sourcesInput = ref<InstanceType<typeof TagInput> | null>(null)

const editing = computed(() => props.rule !== null)
/** Opened by another feature: only the sources can change. */
const managed = computed(() => !!props.rule?.owner)

watch(open, (value) => {
  if (!value) return
  const rule = props.rule
  Object.assign(form, {
    action: rule?.action ?? 'allow',
    protocol: rule?.protocol ?? 'tcp',
    ports: [...(rule?.ports ?? [])],
    sources: [...(rule?.sources ?? [])],
    note: rule?.note ?? '',
  })
  errors.value = {}
  error.value = null
})

const actions = computed(() => [
  { value: 'allow' as const, label: t('firewall.action.allow') },
  { value: 'deny' as const, label: t('firewall.action.deny') },
])
const protocols = computed(() => RULE_PROTOCOLS.map((value) => ({ value, label: protocolLabel(value) })))

const title = computed(() => {
  if (managed.value) return t('firewall.rule.sourcesTitle')
  return editing.value ? t('firewall.rule.editTitle') : t('firewall.rule.createTitle')
})

const steps = computed(() => {
  const ports = form.ports.length > 0 ? form.ports.join(', ') : '…'
  const sources = form.sources.length > 0 ? form.sources.join(', ') : t('firewall.rule.steps.anywhere')
  return [
    t(`firewall.rule.steps.${form.action}`, { protocol: protocolLabel(form.protocol), ports, sources }),
    t('firewall.confirm.step'),
  ]
})

function addMine() {
  if (props.clientIp && !form.sources.includes(props.clientIp)) form.sources = [...form.sources, props.clientIp]
}

function validate(): boolean {
  const next: typeof errors.value = {}
  if (form.ports.length === 0) next.ports = validationMessage('ports', 'required')
  else if (form.ports.length > PORTS_MAX) next.ports = validationMessage('ports', 'count')
  else if (!form.ports.every(isPortSpec)) next.ports = validationMessage('ports', 'port_spec')
  if (form.sources.length > SOURCES_MAX) next.sources = validationMessage('sources', 'count')
  else if (!form.sources.every(isIpOrCidr)) next.sources = validationMessage('sources', 'ip')
  // Inbound traffic is refused by default: a deny rule only makes sense for some sources.
  else if (form.action === 'deny' && form.sources.length === 0) next.sources = t('errors.firewall.deny_needs_source')
  if (textLength(form.note.trim()) > NOTE_MAX) next.note = validationMessage('note', 'max_length', { max: NOTE_MAX })
  errors.value = next
  return Object.keys(next).length === 0
}

/** Server errors that belong to one field are shown under it. */
function placeError(err: unknown): boolean {
  if (!isApiError(err)) return false
  const field = err.code === 'validation.invalid' ? err.params.field : err.code === 'firewall.deny_needs_source' ? 'sources' : ''
  if (field !== 'ports' && field !== 'sources' && field !== 'note') return false
  errors.value = { ...errors.value, [field]: errorMessage(err) }
  return true
}

async function submit() {
  portsInput.value?.commit()
  sourcesInput.value?.commit()
  if (!validate()) return
  const body: RuleRequest = {
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
    const before = props.rule
    if (before) {
      const result = await updateRule(before.id, body)
      const previous = ruleRequest(before)
      emit('changed', result, {
        summary: t('firewall.summary.ruleUpdate', { rule: describeRule(body) }),
        undo: () => updateRule(before.id, previous),
      })
    } else {
      const result = await addRule(body)
      emit('changed', result, {
        summary: t('firewall.summary.ruleAdd', { rule: describeRule(body) }),
        undo: () => deleteRule(result.rule.id),
      })
    }
    open.value = false
  } catch (err) {
    if (isStepUpRequired(err)) return
    if (!placeError(err)) error.value = err
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <FormSheet
    v-model:open="open"
    :title="title"
    :description="managed ? undefined : t('firewall.rule.intro')"
    :submit-label="editing ? t('firewall.rule.save') : t('firewall.rule.add')"
    :submitting="submitting"
    :steps="steps"
    @submit="submit"
  >
    <Notice v-if="managed && rule" tone="info">{{ t('firewall.rule.managedNote', { owner: ownerLabel(rule.owner) }) }}</Notice>

    <div class="flex flex-wrap gap-x-6 gap-y-4">
      <div class="flex flex-col gap-1.5">
        <span id="rule-action-label" class="text-detail font-medium">{{ t('common.field.action') }}</span>
        <SegmentedControl
          v-model="form.action"
          :options="actions"
          :label="t('common.field.action')"
          :disabled="managed"
          class="w-44"
          aria-labelledby="rule-action-label"
        />
      </div>
      <div class="flex flex-col gap-1.5">
        <span id="rule-protocol-label" class="text-detail font-medium">{{ t('common.field.protocol') }}</span>
        <SegmentedControl
          v-model="form.protocol"
          :options="protocols"
          :label="t('common.field.protocol')"
          :disabled="managed"
          class="w-64 max-w-full"
          aria-labelledby="rule-protocol-label"
        />
      </div>
    </div>

    <FormField id="rule-ports" :label="t('common.field.ports')" :hint="t('firewall.rule.portsHint', { max: PORTS_MAX })" :error="errors.ports">
      <template #default="field">
        <TagInput
          ref="portsInput"
          v-bind="field"
          v-model="form.ports"
          :valid="isPortSpec"
          :max="PORTS_MAX"
          :disabled="managed"
          mono
          placeholder="8080, 8000-8100"
        />
      </template>
    </FormField>

    <FormField
      id="rule-sources"
      :label="t('common.field.sources')"
      :hint="form.action === 'deny' ? t('firewall.rule.denyHint') : t('firewall.rule.sourcesHint')"
      :error="errors.sources"
    >
      <template #default="field">
        <TagInput
          ref="sourcesInput"
          v-bind="field"
          v-model="form.sources"
          :valid="isIpOrCidr"
          :max="SOURCES_MAX"
          mono
          placeholder="10.0.0.0/8"
        />
        <Button
          v-if="clientIp && !form.sources.includes(clientIp)"
          type="button"
          variant="ghost"
          size="sm"
          class="self-start max-md:h-11"
          @click="addMine"
        >
          <UserRound aria-hidden="true" />
          {{ t('firewall.rule.addMine', { ip: clientIp }) }}
        </Button>
      </template>
    </FormField>

    <FormField id="rule-note" :label="t('common.field.note')" :error="errors.note">
      <template #default="field">
        <Input v-bind="field" v-model="form.note" :maxlength="NOTE_MAX" autocomplete="off" :disabled="managed" />
      </template>
    </FormField>

    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
