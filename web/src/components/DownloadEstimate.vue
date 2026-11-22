<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ProbeResult } from '@/api/software'
import { formatBytes, formatDuration } from '@/i18n/format'
import { estimateDownload } from '@/software/estimate'

// How much the probed lines download and roughly how long it takes; parts
// that cannot be estimated say so instead of guessing.
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
  <dl class="m-0 grid grid-cols-[max-content_minmax(0,1fr)] gap-x-4 gap-y-1 rounded-md border px-3 py-2.5 text-detail">
    <template v-if="estimate.hasPackages">
      <dt class="text-muted-foreground">{{ t('software.estimate.packages') }}</dt>
      <dd class="m-0 tabular-nums">
        {{
          estimate.packageBytes === null
            ? t('software.estimate.unknown')
            : t(
                'software.estimate.packageValue',
                { size: formatBytes(estimate.packageBytes), count: estimate.packageCount },
                estimate.packageCount,
              )
        }}
        <span v-if="estimate.partial" class="block text-xs text-muted-foreground">{{ t('software.estimate.partial') }}</span>
      </dd>
    </template>
    <template v-if="estimate.hasRuntimes">
      <dt class="text-muted-foreground">{{ t('software.estimate.runtimes') }}</dt>
      <dd class="m-0 tabular-nums">
        {{ estimate.runtimeUnknown && estimate.runtimeBytes === 0 ? t('software.estimate.unknown') : formatBytes(estimate.runtimeBytes) }}
      </dd>
    </template>
    <dt class="text-muted-foreground">{{ t('software.estimate.total') }}</dt>
    <dd class="m-0 font-semibold tabular-nums">{{ total }}</dd>
    <dt class="text-muted-foreground">{{ t('software.estimate.time') }}</dt>
    <dd class="m-0 font-semibold tabular-nums">{{ time }}</dd>
  </dl>
</template>
