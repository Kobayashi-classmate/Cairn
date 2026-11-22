<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import Card from '@/components/common/Card.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import SelectField from '@/components/common/SelectField.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import type { SelectOption } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useClock } from '@/composables/useClock'
import { formatDate } from '@/i18n/format'
import { useCertsStore } from '@/stores/certs'
import { useSessionStore } from '@/stores/session'
import { askPanelCert, canServePanel } from '@/views/certs/actions'
import SettingRow from './SettingRow.vue'
import SettingRows from './SettingRows.vue'

// The certificate the panel serves: a valid certificate from the
// certificates page, or its own self-signed one. Switching is a job (the
// job dock follows it) and asks to re-authenticate; the panel keeps running.
const { t } = useI18n()
const session = useSessionStore()
const store = useCertsStore()
const now = useClock()

const canManage = computed(() => session.can('certs.manage'))
const list = computed(() => store.list)
const chosen = ref('')

onMounted(() => void store.load())

// Follow the panel's certificate as it changes (after a switch, elsewhere).
watch(
  () => list.value?.panel,
  (panel) => {
    if (panel !== undefined) chosen.value = panel
  },
  { immediate: true },
)

const current = computed(() => list.value?.certs.find((cert) => cert.id === list.value?.panel) ?? null)

/** Only valid, unexpired certificates can be served; "" is the self-signed one. */
const options = computed<SelectOption[]>(() => [
  { value: '', label: t('certs.panel.selfSigned') },
  ...(list.value?.certs ?? [])
    .filter((cert) => canServePanel(cert, now.value) || cert.id === list.value?.panel)
    .map((cert) => ({ value: cert.id, label: `${cert.name} (${cert.domains.join(', ')})` })),
])

const chosenDomain = computed(() => {
  const cert = list.value?.certs.find((c) => c.id === chosen.value)
  return cert?.domains.find((d) => !d.startsWith('*.')) ?? cert?.domains[0] ?? ''
})

async function apply() {
  const target = list.value?.certs.find((cert) => cert.id === chosen.value) ?? null
  const ok = await askPanelCert(target, current.value ?? undefined)
  // Dismissed: show the certificate in use again.
  if (!ok && list.value) chosen.value = list.value.panel
}
</script>

<template>
  <Card :title="t('certs.panel.title')" :level="3" flush>
    <ErrorState v-if="store.loadError && !list" :error="store.loadError" compact @retry="store.load()" />
    <div v-else-if="!list" class="flex flex-col gap-2 px-4 py-4 md:px-[18px]" role="status">
      <span class="sr-only">{{ t('common.state.loading') }}</span>
      <Skeleton class="h-4 w-48" />
      <Skeleton class="h-4 w-72 max-w-full" />
    </div>
    <SettingRows v-else>
      <SettingRow :title="t('settings.panelCert.inUse')" :level="4">
        <template #description>
          <template v-if="current">
            <span dir="auto">{{ current.name }}</span>
            <template v-if="current.notAfter"> · {{ t('settings.panelCert.until', { date: formatDate(current.notAfter) }) }}</template>
          </template>
          <template v-else>{{ t('certs.panel.currentSelfSigned') }}</template>
        </template>
        <template #badge>
          <StatusPill v-if="!current" tone="warning">{{ t('settings.panelCert.selfSignedTag') }}</StatusPill>
        </template>
        <RouterLink
          v-if="current"
          :to="{ name: 'certs', query: { open: `cert:${current.id}` } }"
          class="text-detail text-link underline-offset-2 hover:underline tap-y"
        >
          {{ t('certs.panel.view') }}
        </RouterLink>
      </SettingRow>
      <SettingRow
        v-if="canManage"
        :level="4"
        :title="t('settings.panelCert.choose')"
        :description="chosen && chosenDomain ? t('certs.panel.openBy', { domain: chosenDomain }) : t('certs.panel.noRestart')"
        label-for="panel-cert"
      >
        <SelectField id="panel-cert" v-model="chosen" :options="options" class="w-full @lg:w-72" />
        <Button class="max-md:h-11" :disabled="chosen === list.panel" @click="apply">{{ t('certs.panel.apply') }}</Button>
      </SettingRow>
    </SettingRows>
  </Card>
</template>
