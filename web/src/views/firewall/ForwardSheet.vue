<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { isApiError, isStepUpRequired } from '@/api/client'
import {
  NOTE_MAX,
  RULE_PROTOCOLS,
  addForward,
  deleteForward,
  isForwardTarget,
  isPort,
  updateForward,
  type ChangeResult,
  type FirewallForward,
  type ForwardRequest,
  type RuleProtocol,
} from '@/api/firewall'
import FormField from '@/components/common/FormField.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import SegmentedControl from '@/components/common/SegmentedControl.vue'
import { Input } from '@/components/ui/input'
import type { ChangeInfo } from '@/composables/useFirewallConfirm'
import { textLength } from '@/forms/rules'
import { errorMessage, errorText, validationMessage } from '@/i18n/errors'
import { describeForward, forwardRequest, protocolLabel } from './labels'

// Adds or changes a port forward: connections to a port of this server go
// on to another port of this host or of another IPv4 host.
const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{
  /** The forward to change, or null for a new one. */
  forward: FirewallForward | null
  /** The other forwards, to catch a port that is taken. */
  forwards: readonly FirewallForward[]
  /** The kernel forwards IPv4 now. */
  ipForward: boolean
  /** Ports kept for SSH and the panel (TCP). */
  keptPorts: readonly number[]
}>()
const emit = defineEmits<{ changed: [result: ChangeResult, info: ChangeInfo] }>()

type Field = 'port' | 'toAddress' | 'toPort' | 'note'
type Target = 'local' | 'remote'

const { t } = useI18n()
const form = reactive({
  protocol: 'tcp' as RuleProtocol,
  port: '',
  target: 'local' as Target,
  toAddress: '',
  toPort: '',
  note: '',
})
const errors = ref<Partial<Record<Field, string>>>({})
const submitting = ref(false)
const error = ref<unknown>(null)

const editing = computed(() => props.forward !== null)

watch(open, (value) => {
  if (!value) return
  const forward = props.forward
  Object.assign(form, {
    protocol: forward?.protocol ?? 'tcp',
    port: forward ? String(forward.port) : '',
    target: forward?.toAddress ? 'remote' : 'local',
    toAddress: forward?.toAddress ?? '',
    toPort: forward ? String(forward.toPort) : '',
    note: forward?.note ?? '',
  })
  errors.value = {}
  error.value = null
})

const protocols = computed(() => RULE_PROTOCOLS.map((value) => ({ value, label: protocolLabel(value) })))
const targets = computed(() => [
  { value: 'local' as const, label: t('firewall.forward.thisHost') },
  { value: 'remote' as const, label: t('firewall.forward.otherHost') },
])

function number(value: string): number | null {
  return /^\d{1,5}$/.test(value.trim()) ? Number(value.trim()) : null
}

const port = computed(() => number(form.port))
const toPort = computed(() => number(form.toPort))
const remote = computed(() => form.target === 'remote')

/** What the form would send. */
const request = computed<ForwardRequest>(() => {
  const body: ForwardRequest = { protocol: form.protocol, port: port.value ?? 0, toPort: toPort.value ?? 0 }
  if (remote.value) body.toAddress = form.toAddress.trim()
  if (form.note.trim()) body.note = form.note.trim()
  return body
})

const steps = computed(() => {
  const body = request.value
  const list = [
    t('firewall.forward.steps.forward', {
      protocol: protocolLabel(body.protocol),
      port: port.value ?? '…',
      target: remote.value
        ? `${body.toAddress || '…'}:${toPort.value ?? '…'}`
        : t('firewall.forward.thisHostPort', { port: toPort.value ?? '…' }),
    }),
    t('firewall.forward.steps.accept'),
  ]
  if (remote.value) {
    if (!props.ipForward) list.push(t('firewall.forward.steps.ipForward'))
    list.push(t('firewall.forward.steps.masquerade'))
  }
  list.push(t('firewall.confirm.step'))
  return list
})

/** Two forwards may not share a port and a protocol. */
function sharesProtocol(a: RuleProtocol, b: RuleProtocol): boolean {
  return a === b || a === 'both' || b === 'both'
}

function validate(): boolean {
  const next: typeof errors.value = {}
  const listen = port.value
  const target = toPort.value
  if (listen === null || !isPort(listen)) next.port = validationMessage('port', 'port')
  else if (form.protocol !== 'udp' && props.keptPorts.includes(listen)) {
    next.port = errorText('firewall.protected', { port: listen }) ?? validationMessage('port', 'port')
  } else if (
    props.forwards.some((other) => other.id !== props.forward?.id && other.port === listen && sharesProtocol(other.protocol, form.protocol))
  ) {
    next.port = errorText('firewall.forward_taken', { port: listen }) ?? validationMessage('port', 'port')
  }
  if (remote.value) {
    const address = form.toAddress.trim()
    if (!address) next.toAddress = validationMessage('toAddress', 'required')
    else if (!isForwardTarget(address)) next.toAddress = validationMessage('toAddress', 'forward_target')
  }
  if (target === null || !isPort(target)) next.toPort = validationMessage('toPort', 'port')
  else if (!remote.value && target === listen) next.toPort = validationMessage('toPort', 'same_port')
  if (textLength(form.note.trim()) > NOTE_MAX) next.note = validationMessage('note', 'max_length', { max: NOTE_MAX })
  errors.value = next
  return Object.keys(next).length === 0
}

/** Server errors that belong to one field are shown under it. */
function placeError(err: unknown): boolean {
  if (!isApiError(err)) return false
  let field: unknown = ''
  if (err.code === 'validation.invalid') field = err.params.field
  else if (err.code === 'firewall.forward_taken' || err.code === 'firewall.protected') field = 'port'
  if (field !== 'port' && field !== 'toAddress' && field !== 'toPort' && field !== 'note') return false
  errors.value = { ...errors.value, [field]: errorMessage(err) }
  return true
}

async function submit() {
  if (!validate()) return
  const body = request.value
  submitting.value = true
  error.value = null
  try {
    // Step-up protected: the client asks for the password and retries.
    const before = props.forward
    if (before) {
      const result = await updateForward(before.id, body)
      const previous = forwardRequest(before)
      emit('changed', result, {
        summary: t('firewall.summary.forwardUpdate', { forward: describeForward(body) }),
        undo: () => updateForward(before.id, previous),
      })
    } else {
      const result = await addForward(body)
      emit('changed', result, {
        summary: t('firewall.summary.forwardAdd', { forward: describeForward(body) }),
        undo: () => deleteForward(result.forward.id),
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
    :title="editing ? t('firewall.forward.editTitle') : t('firewall.forward.createTitle')"
    :description="t('firewall.forward.intro')"
    :submit-label="editing ? t('firewall.forward.save') : t('firewall.forward.add')"
    :submitting="submitting"
    :steps="steps"
    @submit="submit"
  >
    <div class="flex flex-col gap-1.5">
      <span id="forward-protocol-label" class="text-detail font-medium">{{ t('common.field.protocol') }}</span>
      <SegmentedControl
        v-model="form.protocol"
        :options="protocols"
        :label="t('common.field.protocol')"
        class="w-64 max-w-full"
        aria-labelledby="forward-protocol-label"
      />
    </div>

    <FormField id="forward-port" :label="t('firewall.forward.port')" :hint="t('firewall.forward.portHint')" :error="errors.port">
      <template #default="field">
        <Input v-bind="field" v-model="form.port" inputmode="numeric" autocomplete="off" class="w-40 font-mono" placeholder="8080" />
      </template>
    </FormField>

    <div class="flex flex-col gap-1.5">
      <span id="forward-target-label" class="text-detail font-medium">{{ t('firewall.forward.target') }}</span>
      <SegmentedControl
        v-model="form.target"
        :options="targets"
        :label="t('firewall.forward.target')"
        class="w-72 max-w-full"
        aria-labelledby="forward-target-label"
      />
    </div>

    <FormField
      v-if="remote"
      id="forward-address"
      :label="t('common.field.toAddress')"
      :hint="t('firewall.forward.addressHint')"
      :error="errors.toAddress"
    >
      <template #default="field">
        <Input
          v-bind="field"
          v-model="form.toAddress"
          autocomplete="off"
          autocapitalize="none"
          spellcheck="false"
          dir="ltr"
          class="font-mono"
          placeholder="10.0.0.5"
        />
      </template>
    </FormField>

    <FormField
      id="forward-to-port"
      :label="t('common.field.toPort')"
      :hint="remote ? undefined : t('firewall.forward.toPortHint')"
      :error="errors.toPort"
    >
      <template #default="field">
        <Input v-bind="field" v-model="form.toPort" inputmode="numeric" autocomplete="off" class="w-40 font-mono" placeholder="80" />
      </template>
    </FormField>

    <FormField id="forward-note" :label="t('common.field.note')" :error="errors.note">
      <template #default="field">
        <Input v-bind="field" v-model="form.note" :maxlength="NOTE_MAX" autocomplete="off" />
      </template>
    </FormField>

    <Notice v-if="remote" tone="info">{{ t('firewall.forward.ipv6Note') }}</Notice>
    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
  </FormSheet>
</template>
