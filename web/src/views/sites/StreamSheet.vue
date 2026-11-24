<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { isApiError, isStepUpRequired } from '@/api/client'
import { createStream, updateStream, type StreamProtocol, type StreamRequest, type StreamView } from '@/api/streams'
import FormField from '@/components/common/FormField.vue'
import FormSheet from '@/components/common/FormSheet.vue'
import Notice from '@/components/common/Notice.vue'
import SegmentedControl from '@/components/common/SegmentedControl.vue'
import SwitchField from '@/components/common/SwitchField.vue'
import TagInput from '@/components/common/TagInput.vue'
import { Input } from '@/components/ui/input'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { MAX_UPSTREAMS, isAddressUpstream, isHostPort, isLocalUpstream, isSiteCode, isStreamPort } from '@/sites/validate'
import { useSessionStore } from '@/stores/session'
import { runStreamJob } from './actions'

// Create or change a four-layer proxy (step-up protected: the proxy listens
// on a port open to the network). An upstream on this host publishes one of
// its services, which is the firewall's decision: without `firewall.manage`
// upstreams are IP addresses of other hosts only (a name could point here).
const open = defineModel<boolean>('open', { required: true })
/** The proxy to change, or null to create one. */
const props = defineProps<{ stream: StreamView | null }>()

const { t } = useI18n()
const session = useSessionStore()
const canLocal = computed(() => session.can('firewall.manage'))

const form = reactive({
  code: '',
  protocol: 'tcp' as StreamProtocol,
  port: '',
  upstreams: [] as string[],
  hash: false,
})
const errors = ref<Partial<Record<'code' | 'port' | 'upstreams', string>>>({})
const submitting = ref(false)
const error = ref<unknown>(null)
const upstreamsInput = ref<InstanceType<typeof TagInput> | null>(null)

const editing = computed(() => props.stream !== null)

watch(open, (value) => {
  if (!value) return
  const stream = props.stream
  Object.assign(form, {
    code: '',
    protocol: stream?.protocol ?? 'tcp',
    port: stream ? String(stream.port) : '',
    upstreams: [...(stream?.upstreams ?? [])],
    hash: stream?.balance === 'hash',
  })
  errors.value = {}
  error.value = null
})

const protocols = [
  { value: 'tcp' as const, label: 'TCP' },
  { value: 'udp' as const, label: 'UDP' },
]

const port = computed(() => (/^\d+$/.test(form.port.trim()) ? Number(form.port.trim()) : null))
const derivedCode = computed(() => (port.value ? `${form.protocol}-${port.value}` : ''))

const steps = computed(() => {
  const listen = port.value ? `${form.protocol.toUpperCase()} ${port.value}` : form.protocol.toUpperCase()
  return [
    t('streams.steps.config', { listen, count: form.upstreams.length }, form.upstreams.length),
    t('streams.steps.reload'),
  ]
})

function normalize(value: string): string {
  return value.trim().toLowerCase()
}

function validate(): boolean {
  const next: typeof errors.value = {}
  if (!editing.value && form.code.trim() && !isSiteCode(form.code.trim())) next.code = validationMessage('siteCode', 'site_code')
  if (port.value === null || !isStreamPort(port.value)) next.port = validationMessage('port', 'port')
  if (form.upstreams.length === 0) next.upstreams = validationMessage('upstreams', 'required')
  else if (form.upstreams.length > MAX_UPSTREAMS) next.upstreams = validationMessage('upstreams', 'count')
  else if (!form.upstreams.every(isHostPort)) next.upstreams = validationMessage('upstreams', 'host_port')
  else if (!canLocal.value) {
    const named = form.upstreams.find((upstream) => !isAddressUpstream(upstream))
    const local = form.upstreams.find(isLocalUpstream)
    // The same messages as the server's refusals.
    if (named) next.upstreams = t('errors.detailed.stream.upstream_name', { upstream: named })
    else if (local) next.upstreams = t('errors.detailed.stream.local_upstream', { upstream: local })
  }
  errors.value = next
  return Object.keys(next).length === 0
}

async function submit() {
  upstreamsInput.value?.commit()
  if (!validate()) return
  const request: StreamRequest = { protocol: form.protocol, port: port.value ?? 0, upstreams: [...form.upstreams] }
  if (form.hash) request.balance = 'hash'
  if (!editing.value && form.code.trim()) request.code = form.code.trim()
  submitting.value = true
  error.value = null
  try {
    const current = props.stream
    // Step-up protected: the client asks for the password and retries.
    await runStreamJob(() => (current ? updateStream(current.code, request) : createStream(request)), { rethrow: true })
    open.value = false
  } catch (err) {
    if (isStepUpRequired(err)) return
    // Errors about the upstreams or the port go to their fields.
    const code = isApiError(err) ? err.code : ''
    if (code === 'stream.local_upstream' || code === 'stream.upstream_name') errors.value = { ...errors.value, upstreams: errorMessage(err) }
    else if (code === 'stream.port_taken' || code === 'stream.selinux_port') errors.value = { ...errors.value, port: errorMessage(err) }
    else error.value = err
  } finally {
    submitting.value = false
  }
}

/** nginx's stream module comes with nginx from the software page. */
const moduleMissing = computed(() => isApiError(error.value) && error.value.code === 'stream.module_missing')
</script>

<template>
  <FormSheet
    v-model:open="open"
    :title="editing ? t('streams.dialog.editTitle', { code: stream?.code ?? '' }) : t('streams.dialog.createTitle')"
    :description="t('streams.intro')"
    :submit-label="editing ? t('streams.dialog.save') : t('streams.dialog.create')"
    :submitting="submitting"
    :steps="steps"
    @submit="submit"
  >
    <div class="flex flex-col gap-1.5">
      <span id="stream-protocol-label" class="text-detail font-medium">{{ t('common.field.protocol') }}</span>
      <SegmentedControl
        v-model="form.protocol"
        :options="protocols"
        :label="t('common.field.protocol')"
        class="w-40"
        aria-labelledby="stream-protocol-label"
      />
    </div>

    <FormField id="stream-port" :label="t('common.field.port')" :hint="t('streams.dialog.portHint')" :error="errors.port">
      <template #default="field">
        <Input v-bind="field" v-model="form.port" inputmode="numeric" autocomplete="off" class="w-40 font-mono" />
      </template>
    </FormField>

    <FormField v-if="!editing" id="stream-code" :label="t('streams.code')" :hint="t('streams.dialog.codeHint')" :error="errors.code">
      <template #default="field">
        <Input
          v-bind="field"
          v-model="form.code"
          maxlength="20"
          autocomplete="off"
          spellcheck="false"
          :placeholder="derivedCode"
          class="font-mono"
        />
      </template>
    </FormField>

    <FormField
      id="stream-upstreams"
      :label="t('common.field.upstreams')"
      :hint="`${t('streams.dialog.upstreamsHint')} ${canLocal ? t('streams.dialog.localHint') : t('streams.dialog.localAdminHint')}`"
      :error="errors.upstreams"
    >
      <template #default="field">
        <TagInput
          ref="upstreamsInput"
          v-bind="field"
          v-model="form.upstreams"
          :normalize="normalize"
          :valid="isHostPort"
          :max="MAX_UPSTREAMS"
          mono
          :placeholder="canLocal ? '127.0.0.1:3306' : '10.0.0.5:3306'"
        />
      </template>
    </FormField>

    <SwitchField v-model="form.hash" :label="t('streams.dialog.hash')" :hint="t('streams.dialog.hashHint')" />

    <Notice tone="info">{{ t('streams.firewallNote') }}</Notice>
    <Notice v-if="error" tone="destructive">
      {{ errorMessage(error) }}
      <template v-if="moduleMissing" #action>
        <RouterLink :to="{ name: 'software' }" class="underline underline-offset-2">{{ t('sites.installNginx') }}</RouterLink>
      </template>
    </Notice>
  </FormSheet>
</template>
