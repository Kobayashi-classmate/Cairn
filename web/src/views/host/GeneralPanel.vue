<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { LoaderCircle, Power } from '@lucide/vue'
import { isStepUpRequired } from '@/api/client'
import { fetchTimezones, setHostname, setNtp, setTimezone, type HostView } from '@/api/host'
import Card from '@/components/common/Card.vue'
import ComboboxField from '@/components/common/ComboboxField.vue'
import KeyValueList from '@/components/common/KeyValueList.vue'
import KeyValueRow from '@/components/common/KeyValueRow.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { notifyError, notifySuccess } from '@/lib/notify'

// Host name, time zone and time sync, and restarting the server.
const props = defineProps<{ view: HostView; canManage: boolean; rebooting: boolean }>()
const emit = defineEmits<{ changed: []; reboot: [] }>()

/** A host name as the server takes it: lower-case labels of letters, digits and hyphens, at most 253 characters. */
const HOSTNAME = /^(?=.{1,253}$)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$/

const { t } = useI18n()

const hostname = ref('')
const savingHostname = ref(false)
const zones = ref<string[]>([])
const zonesLoading = ref(false)
const timezone = ref('')
const savingZone = ref(false)
const savingNtp = ref(false)

watch(
  () => props.view.hostname,
  (value) => (hostname.value = value),
  { immediate: true },
)
watch(
  () => props.view.timezone,
  (value) => (timezone.value = value),
  { immediate: true },
)

/** Host names do not tell case apart: what is typed is saved in lower case. */
const hostnameValue = computed(() => hostname.value.trim().toLowerCase())
const hostnameValid = computed(() => HOSTNAME.test(hostnameValue.value))
const hostnameError = computed(() => (hostname.value.trim() && !hostnameValid.value ? t('errors.host.hostname_invalid') : null))
/** A zone the system knows (once the list is loaded). */
const zoneValid = computed(() => zones.value.length === 0 || zones.value.includes(timezone.value.trim()))
const zoneError = computed(() => (timezone.value.trim() && !zoneValid.value ? t('errors.host.timezone_invalid') : null))

async function loadZones() {
  if (zones.value.length || zonesLoading.value) return
  zonesLoading.value = true
  try {
    zones.value = await fetchTimezones()
  } catch (err) {
    notifyError(err)
  } finally {
    zonesLoading.value = false
  }
}

async function saveHostname() {
  if (!hostnameValid.value) return
  savingHostname.value = true
  try {
    // Step-up protected: the client asks for the password and retries.
    await setHostname(hostnameValue.value)
    notifySuccess(t('host.general.saved'))
    emit('changed')
  } catch (err) {
    if (!isStepUpRequired(err)) notifyError(err)
  } finally {
    savingHostname.value = false
  }
}

async function saveZone() {
  const zone = timezone.value.trim()
  if (!zone || !zoneValid.value) return
  savingZone.value = true
  try {
    await setTimezone(zone)
    notifySuccess(t('host.general.saved'))
    emit('changed')
  } catch (err) {
    notifyError(err)
  } finally {
    savingZone.value = false
  }
}

async function toggleNtp(enabled: boolean) {
  savingNtp.value = true
  try {
    await setNtp(enabled)
    notifySuccess(enabled ? t('host.general.ntpOn') : t('host.general.ntpOff'))
    emit('changed')
  } catch (err) {
    notifyError(err)
  } finally {
    savingNtp.value = false
  }
}

const ntpState = computed(() => {
  const ntp = props.view.ntp
  if (!ntp.available || !ntp.enabled) return null
  return ntp.synchronized ? { tone: 'success' as const, text: t('host.general.synced') } : { tone: 'warning' as const, text: t('host.general.notSynced') }
})
</script>

<template>
  <div class="grid items-start gap-4 lg:grid-cols-2">
    <div class="flex flex-col gap-4">
      <Card :title="t('host.general.hostname')">
        <form class="flex flex-col gap-1.5" novalidate @submit.prevent="saveHostname">
          <label for="host-name" class="sr-only">{{ t('host.general.hostname') }}</label>
          <div class="flex flex-wrap gap-2">
            <Input
              id="host-name"
              v-model="hostname"
              autocomplete="off"
              autocapitalize="none"
              spellcheck="false"
              dir="ltr"
              class="min-w-0 flex-[1_1_200px] font-mono"
              aria-describedby="host-name-hint"
              :aria-invalid="hostnameError ? true : undefined"
              :disabled="!canManage || savingHostname"
            />
            <Button v-if="canManage" type="submit" class="max-md:h-11" :disabled="!hostnameValid || hostnameValue === view.hostname || savingHostname">
              <LoaderCircle v-if="savingHostname" class="animate-spin" aria-hidden="true" />
              {{ t('host.general.saveHostname') }}
            </Button>
          </div>
          <p v-if="hostnameError" class="m-0 text-xs font-medium text-destructive">{{ hostnameError }}</p>
          <p id="host-name-hint" class="m-0 text-xs text-muted-foreground">{{ t('host.general.hostnameHint') }}</p>
        </form>
      </Card>

      <!-- The time zone suggestions open below the field: the card must not clip them. -->
      <Card :title="t('host.general.time')" class="overflow-visible" flush>
        <div class="divide-y">
          <form class="flex flex-col gap-1.5 px-4 py-4 md:px-[18px]" novalidate @submit.prevent="saveZone">
            <label for="host-zone" class="text-detail font-medium">{{ t('common.field.timezone') }}</label>
            <div class="flex flex-wrap gap-2">
              <ComboboxField
                id="host-zone"
                v-model="timezone"
                :options="zones"
                :disabled="!canManage || savingZone"
                :placeholder="view.timezone"
                class="min-w-0 flex-[1_1_220px]"
                :aria-invalid="zoneError ? true : undefined"
                @focusin="loadZones"
              />
              <Button v-if="canManage" type="submit" class="max-md:h-11" :disabled="!timezone.trim() || timezone.trim() === view.timezone || !zoneValid || savingZone">
                <LoaderCircle v-if="savingZone" class="animate-spin" aria-hidden="true" />
                {{ t('host.general.saveZone') }}
              </Button>
            </div>
            <p v-if="zoneError" class="m-0 text-xs font-medium text-destructive">{{ zoneError }}</p>
            <p v-else-if="zonesLoading" class="m-0 text-xs text-muted-foreground">{{ t('host.general.zonesLoading') }}</p>
          </form>
          <div class="flex flex-col gap-2 px-4 py-4 md:px-[18px]">
            <div class="flex flex-wrap items-center gap-3">
              <Switch
                id="host-ntp"
                :model-value="view.ntp.enabled"
                :disabled="!canManage || !view.ntp.available || savingNtp"
                aria-describedby="host-ntp-hint"
                @update:model-value="toggleNtp"
              />
              <label for="host-ntp" class="cursor-pointer text-detail font-medium">{{ t('host.general.ntp') }}</label>
              <StatusPill v-if="ntpState" :tone="ntpState.tone">{{ ntpState.text }}</StatusPill>
            </div>
            <p id="host-ntp-hint" class="m-0 text-xs text-muted-foreground">
              {{ view.ntp.available ? t('host.general.ntpHint') : t('errors.host.ntp_unavailable') }}
            </p>
          </div>
        </div>
      </Card>
    </div>

    <Card :title="t('host.reboot.cardTitle')" flush>
      <KeyValueList>
        <KeyValueRow :label="t('host.reboot.running')" mono>{{ view.kernel.running }}</KeyValueRow>
        <KeyValueRow v-if="view.kernel.latest && view.kernel.latest !== view.kernel.running" :label="t('host.reboot.latest')" mono>
          {{ view.kernel.latest }}
        </KeyValueRow>
        <KeyValueRow :label="t('host.reboot.needed')">
          <StatusPill :tone="view.rebootRequired ? 'warning' : 'neutral'">
            {{ view.rebootRequired ? t('host.reboot.neededYes') : t('host.reboot.neededNo') }}
          </StatusPill>
        </KeyValueRow>
      </KeyValueList>
      <template v-if="canManage" #footer>
        <p class="m-0 min-w-0 flex-1 basis-60 text-xs text-muted-foreground">{{ t('host.reboot.text') }}</p>
        <Button variant="outline" class="text-destructive hover:text-destructive max-md:h-11" :disabled="rebooting" @click="emit('reboot')">
          <LoaderCircle v-if="rebooting" class="animate-spin" aria-hidden="true" />
          <Power v-else aria-hidden="true" />
          {{ t('host.reboot.submit') }}
        </Button>
      </template>
    </Card>
  </div>
</template>
