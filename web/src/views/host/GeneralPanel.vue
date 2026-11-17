<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Host name, time zone and time sync, and rebooting.
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NCard, NFormItem, NInput, NSelect, NSwitch, NTag, NText, useMessage } from 'naive-ui'
import { isStepUpRequired } from '@/api/client'
import { fetchHost, fetchTimezones, rebootHost, setHostname, setNtp, setTimezone, type HostView } from '@/api/host'
import { useConfirm } from '@/composables/useConfirm'
import { useErrorToast } from '@/composables/useErrorToast'
import { errorMessage } from '@/i18n/errors'

const props = defineProps<{ view: HostView; canManage: boolean }>()
const emit = defineEmits<{ changed: [] }>()

/** A host name: labels of letters, digits and hyphens, at most 253 characters. */
const HOSTNAME = /^(?=.{1,253}$)[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$/
const POLL_MS = 3000
/** After this long the panel counts as back even if it never seemed to go away. */
const ASSUME_BACK_S = 120

const { t } = useI18n()
const message = useMessage()
const confirm = useConfirm()
const showError = useErrorToast()

const hostname = ref('')
const savingHostname = ref(false)
const zones = ref<string[]>([])
const zonesLoading = ref(false)
const timezone = ref<string | null>(null)
const savingZone = ref(false)
const savingNtp = ref(false)
const rebooting = ref(false)
const rebootError = ref<unknown>(null)
let pollTimer: ReturnType<typeof setTimeout> | undefined

watch(
  () => props.view,
  (view) => {
    hostname.value = view.hostname
    timezone.value = view.timezone
  },
  { immediate: true },
)

const hostnameValid = computed(() => HOSTNAME.test(hostname.value.trim()))
const zoneOptions = computed(() => (zones.value.length ? zones.value : [props.view.timezone]).map((zone) => ({ value: zone, label: zone })))

async function loadZones() {
  if (zones.value.length || zonesLoading.value) return
  zonesLoading.value = true
  try {
    zones.value = await fetchTimezones()
  } catch (err) {
    showError(err)
  } finally {
    zonesLoading.value = false
  }
}

async function saveHostname() {
  if (!hostnameValid.value) return
  savingHostname.value = true
  try {
    // Step-up protected: the client asks for the password and retries.
    await setHostname(hostname.value.trim())
    message.success(t('host.general.saved'))
    emit('changed')
  } catch (err) {
    if (!isStepUpRequired(err)) showError(err)
  } finally {
    savingHostname.value = false
  }
}

async function saveZone() {
  if (!timezone.value) return
  savingZone.value = true
  try {
    await setTimezone(timezone.value)
    message.success(t('host.general.saved'))
    emit('changed')
  } catch (err) {
    showError(err)
  } finally {
    savingZone.value = false
  }
}

async function toggleNtp(enabled: boolean) {
  savingNtp.value = true
  try {
    await setNtp(enabled)
    emit('changed')
  } catch (err) {
    showError(err)
  } finally {
    savingNtp.value = false
  }
}

/** Waits for the panel to answer again after the reboot. */
function waitForReturn(at: number) {
  let sawDown = false
  const poll = async () => {
    try {
      await fetchHost()
      const now = Date.now() / 1000
      if (sawDown || now > at + ASSUME_BACK_S) {
        rebooting.value = false
        message.success(t('host.reboot.back'))
        emit('changed')
        return
      }
    } catch {
      sawDown = true
    }
    pollTimer = setTimeout(() => void poll(), POLL_MS)
  }
  pollTimer = setTimeout(() => void poll(), POLL_MS * 2)
}

async function reboot() {
  const ok = await confirm({
    title: t('host.reboot.title', { host: props.view.hostname }),
    content: t('host.reboot.text'),
    confirmText: t('host.reboot.submit'),
    danger: true,
  })
  if (!ok) return
  rebootError.value = null
  try {
    // Step-up protected: the client asks for the password and retries.
    const { at } = await rebootHost()
    rebooting.value = true
    waitForReturn(at)
  } catch (err) {
    if (!isStepUpRequired(err)) rebootError.value = err
  }
}

onBeforeUnmount(() => clearTimeout(pollTimer))
</script>

<template>
  <div class="cards">
    <NCard size="small" :title="t('host.general.hostname')">
      <div class="inline">
        <NInput
          v-model:value="hostname"
          :disabled="!canManage"
          :status="hostname && !hostnameValid ? 'error' : undefined"
          :input-props="{ autocomplete: 'off', spellcheck: false }"
        />
        <NButton
          v-if="canManage"
          type="primary"
          :loading="savingHostname"
          :disabled="!hostnameValid || hostname.trim() === view.hostname"
          @click="saveHostname"
        >
          {{ t('common.action.save') }}
        </NButton>
      </div>
      <NText v-if="hostname && !hostnameValid" type="error" tag="p" class="hint">{{ t('errors.host.hostname_invalid') }}</NText>
      <NText v-else depth="3" tag="p" class="hint">{{ t('host.general.hostnameHint') }}</NText>
    </NCard>

    <NCard size="small" :title="t('host.general.time')">
      <NFormItem :label="t('common.field.timezone')" :show-feedback="false">
        <div class="inline">
          <NSelect
            v-model:value="timezone"
            :options="zoneOptions"
            filterable
            :loading="zonesLoading"
            :disabled="!canManage"
            @focus="loadZones"
          />
          <NButton v-if="canManage" type="primary" :loading="savingZone" :disabled="!timezone || timezone === view.timezone" @click="saveZone">
            {{ t('common.action.save') }}
          </NButton>
        </div>
      </NFormItem>
      <div class="ntp">
        <label class="switch">
          <NSwitch
            :value="view.ntp.enabled"
            :disabled="!canManage || !view.ntp.available"
            :loading="savingNtp"
            @update:value="toggleNtp"
          />
          <span>{{ t('host.general.ntp') }}</span>
        </label>
        <NTag v-if="view.ntp.available && view.ntp.enabled" size="small" :type="view.ntp.synchronized ? 'success' : 'warning'" :bordered="false">
          {{ view.ntp.synchronized ? t('host.general.synced') : t('host.general.notSynced') }}
        </NTag>
      </div>
      <NText v-if="!view.ntp.available" depth="3" tag="p" class="hint">{{ t('errors.host.ntp_unavailable') }}</NText>
    </NCard>

    <NCard size="small" :title="t('host.reboot.cardTitle')">
      <NAlert v-if="view.rebootRequired" type="warning" class="block">{{ t('host.reboot.required') }}</NAlert>
      <dl class="facts">
        <dt>{{ t('host.reboot.running') }}</dt>
        <dd class="mono">{{ view.kernel.running }}</dd>
        <template v-if="view.kernel.latest && view.kernel.latest !== view.kernel.running">
          <dt>{{ t('host.reboot.latest') }}</dt>
          <dd class="mono">{{ view.kernel.latest }}</dd>
        </template>
      </dl>
      <NAlert v-if="rebooting" type="info" class="block">{{ t('host.reboot.waiting') }}</NAlert>
      <NAlert v-if="rebootError" type="error" class="block">{{ errorMessage(rebootError) }}</NAlert>
      <div v-if="canManage" class="actions">
        <NButton type="error" ghost :loading="rebooting" :disabled="rebooting" @click="reboot">{{ t('host.reboot.submit') }}</NButton>
      </div>
    </NCard>
  </div>
</template>

<style scoped>
.cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(320px, 1fr));
  gap: 12px;
}

.inline {
  display: flex;
  gap: 8px;
  inline-size: 100%;
}

.hint {
  margin: 8px 0 0;
  font-size: 12px;
}

.ntp {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-block-start: 12px;
}

.switch {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
}

.facts {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 4px 12px;
  margin: 0 0 12px;
  font-size: 13px;
}

.facts dt {
  opacity: 0.7;
}

.facts dd {
  margin: 0;
  overflow-wrap: anywhere;
}

.block {
  margin-block-end: 12px;
}

.actions {
  display: flex;
  justify-content: flex-end;
}

.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}
</style>
