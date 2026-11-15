<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { NText } from 'naive-ui'
import type { ProbeResult } from '@/api/software'
import { formatBytes, formatDuration } from '@/i18n/format'
import { estimateDownload } from '@/software/estimate'

const props = defineProps<{
  result: ProbeResult
  /** Chosen candidate per runtime slot id. */
  selection: Record<string, string>
}>()

const { t } = useI18n()

const estimate = computed(() => estimateDownload(props.result, props.selection))

const total = computed(() => {
  const e = estimate.value
  if (e.totalBytes === 0 && !e.complete) return t('software.estimate.unknown')
  const size = formatBytes(e.totalBytes)
  return e.complete ? t('software.estimate.about', { value: size }) : t('software.estimate.atLeast', { value: size })
})

const time = computed(() => {
  const seconds = estimate.value.seconds
  if (seconds === null) return t('software.estimate.unknown')
  return t('software.estimate.about', { value: formatDuration(Math.max(1, Math.ceil(seconds))) })
})
</script>

<template>
  <dl class="estimate">
    <template v-if="estimate.hasPackages">
      <dt>{{ t('software.estimate.packages') }}</dt>
      <dd>
        {{
          estimate.packageBytes === null
            ? t('software.estimate.unknown')
            : t(
                'software.estimate.packageValue',
                { size: formatBytes(estimate.packageBytes), count: estimate.packageCount },
                estimate.packageCount,
              )
        }}
        <NText v-if="estimate.partial" depth="3" tag="div" class="estimate__note">
          {{ t('software.estimate.partial') }}
        </NText>
      </dd>
    </template>
    <template v-if="estimate.hasRuntimes">
      <dt>{{ t('software.estimate.runtimes') }}</dt>
      <dd>
        {{
          estimate.runtimeUnknown && estimate.runtimeBytes === 0
            ? t('software.estimate.unknown')
            : formatBytes(estimate.runtimeBytes)
        }}
      </dd>
    </template>
    <dt>{{ t('software.estimate.total') }}</dt>
    <dd class="estimate__strong">{{ total }}</dd>
    <dt>{{ t('software.estimate.time') }}</dt>
    <dd class="estimate__strong">{{ time }}</dd>
  </dl>
</template>

<style scoped>
.estimate {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 4px 16px;
  margin: 0;
  font-size: 13px;
}

.estimate dt {
  opacity: 0.7;
}

.estimate dd {
  margin: 0;
}

.estimate__strong {
  font-weight: 600;
}

.estimate__note {
  font-size: 12px;
  font-weight: normal;
}
</style>
