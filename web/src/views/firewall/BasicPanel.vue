<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { FirewallView } from '@/api/firewall'
import Card from '@/components/common/Card.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import KeyValueList from '@/components/common/KeyValueList.vue'
import KeyValueRow from '@/components/common/KeyValueRow.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import SwitchField from '@/components/common/SwitchField.vue'
import type { Tone } from '@/lib/tone'

// Basic settings: how the firewall stands, ping, IPv4 forwarding and the
// rules on the system that the panel did not make.
const props = defineProps<{
  view: FirewallView
  canManage: boolean
  /** Changes are possible right now (firewall on, nothing pending). */
  writable: boolean
  /** A ping change is being sent. */
  working: boolean
}>()

const emit = defineEmits<{ ping: [block: boolean] }>()

const { t } = useI18n()

const state = computed<{ tone: Tone; text: string }>(() => {
  const view = props.view
  if (!view.enabled) return { tone: 'neutral', text: t('firewall.state.disabled') }
  return view.loaded ? { tone: 'success', text: t('firewall.state.enabled') } : { tone: 'warning', text: t('firewall.state.notLoaded') }
})

const alwaysOpen = computed(() =>
  t('firewall.basic.alwaysOpenValue', {
    ssh: props.view.sshPorts.map((port) => `${port}/tcp`).join(', '),
    panel: `${props.view.panelPort}/tcp`,
  }),
)

const blockPing = computed({
  get: () => props.view.blockPing,
  set: (value: boolean) => {
    if (value !== props.view.blockPing) emit('ping', value)
  },
})
</script>

<template>
  <div class="grid gap-4 lg:grid-cols-2">
    <Card :title="t('firewall.basic.title')" flush>
      <KeyValueList>
        <KeyValueRow :label="t('firewall.basic.state')">
          <StatusDot :tone="state.tone">{{ state.text }}</StatusDot>
        </KeyValueRow>
        <KeyValueRow :label="t('firewall.basic.backend')">
          <template v-if="view.backend">
            <span class="font-mono text-[12.5px]">{{ view.backend }}</span>
            <span v-if="!view.installed" class="text-muted-foreground">{{ t('firewall.backendMissing') }}</span>
          </template>
          <span v-else class="text-muted-foreground">{{ t('firewall.status.noBackend') }}</span>
        </KeyValueRow>
        <KeyValueRow :label="t('firewall.basic.incoming')">{{ t('firewall.basic.incomingValue') }}</KeyValueRow>
        <KeyValueRow :label="t('firewall.basic.alwaysOpen')" mono>{{ alwaysOpen }}</KeyValueRow>
        <KeyValueRow :label="t('firewall.basic.clientIp')" mono>
          <span dir="ltr">{{ view.clientIp }}</span>
        </KeyValueRow>
      </KeyValueList>
    </Card>

    <div class="flex flex-col gap-4">
      <Card :title="t('firewall.basic.ping')">
        <SwitchField
          v-model="blockPing"
          :label="t('firewall.blockPing')"
          :hint="view.enabled || !canManage ? t('firewall.basic.pingHint') : t('firewall.enableFirst')"
          :disabled="!canManage || !writable || working"
        />
      </Card>

      <Card :title="t('firewall.basic.forwarding')" flush>
        <KeyValueList>
          <KeyValueRow :label="t('firewall.basic.ipForward')">
            <StatusDot :tone="view.ipForward ? 'success' : 'neutral'">
              {{ view.ipForward ? t('firewall.basic.on') : t('firewall.basic.off') }}
            </StatusDot>
          </KeyValueRow>
        </KeyValueList>
        <p class="m-0 border-t px-4 py-3 text-detail text-muted-foreground md:px-[18px]">{{ t('firewall.basic.ipForwardHint') }}</p>
      </Card>
    </div>

    <Card :title="t('firewall.external.title', { count: view.external.length })" :description="t('firewall.external.intro')" class="lg:col-span-2" flush>
      <EmptyState v-if="view.external.length === 0" compact :title="t('firewall.external.empty')" />
      <pre
        v-else
        class="m-0 max-h-[min(50vh,420px)] overflow-auto px-4 py-3 font-mono text-xs leading-relaxed whitespace-pre md:px-[18px]"
        dir="ltr"
        tabindex="0"
        :aria-label="t('firewall.external.title', { count: view.external.length })"
      >{{ view.external.join('\n') }}</pre>
    </Card>
  </div>
</template>
