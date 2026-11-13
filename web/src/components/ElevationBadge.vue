<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { NIcon, NTag, NTooltip } from 'naive-ui'
import { ShieldCheckmarkOutline } from '@vicons/ionicons5'
import { useNow } from '@/composables/useNow'
import { formatTime } from '@/i18n/format'
import { useSessionStore } from '@/stores/session'

const { t } = useI18n()
const session = useSessionStore()
const now = useNow(5000)

// Shown while sensitive actions do not ask for the password again.
const active = computed(() => session.elevatedUntil > now.value)
</script>

<template>
  <NTooltip v-if="active">
    <template #trigger>
      <NTag type="success" size="small" round :bordered="false">
        <template #icon>
          <NIcon><ShieldCheckmarkOutline /></NIcon>
        </template>
        {{ t('common.elevated.label') }}
      </NTag>
    </template>
    {{ t('common.elevated.tooltip', { time: formatTime(session.elevatedUntil, false) }) }}
  </NTooltip>
</template>
