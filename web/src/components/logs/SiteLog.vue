<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, toRef, type HTMLAttributes } from 'vue'
import { useI18n } from 'vue-i18n'
import type { SiteLogKind } from '@/api/sites'
import LogView from '@/components/common/LogView.vue'
import SegmentedControl from '@/components/common/SegmentedControl.vue'
import { Switch } from '@/components/ui/switch'
import { useSiteLog } from '@/composables/useSiteLog'
import { stripAnsi } from '@/logs/ansi'
import AccessLogTable from './AccessLogTable.vue'
import LogText from './LogText'

// A site's nginx access or error log, followed while switched on: the
// access log as a table coloured by status code, the error log as text
// with its levels coloured.
// Shared by the site drawer and the Logs page (which adds a site picker in
// the `controls` slot).
const props = defineProps<{
  code: string
  boxClass?: HTMLAttributes['class']
}>()

const kind = defineModel<SiteLogKind>('kind', { default: 'access' })
const follow = defineModel<boolean>('follow', { default: true })

const { t } = useI18n()
const { lines, error, loading, dropped, retry } = useSiteLog(toRef(props, 'code'), kind, follow)

const kinds = computed(() => [
  { value: 'access' as const, label: t('sites.logs.access') },
  { value: 'error' as const, label: t('sites.logs.error') },
])

const label = computed(() => (kind.value === 'access' ? t('sites.logs.access') : t('sites.logs.error')))
</script>

<template>
  <div class="flex min-w-0 flex-col gap-3">
    <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
      <slot name="controls" />
      <SegmentedControl v-model="kind" :options="kinds" :label="t('sites.logs.kind')" class="w-full sm:w-56" />
      <label class="inline-flex cursor-pointer items-center gap-2 text-detail max-md:min-h-11">
        <Switch v-model="follow" />
        {{ t('sites.logs.follow') }}
      </label>
    </div>
    <LogView
      :label="label"
      :loading="loading"
      :error="error"
      :empty="lines.length === 0"
      :empty-text="t('sites.logs.empty')"
      :copy-text="() => stripAnsi(lines.join('\n'))"
      :box-class="boxClass || undefined"
      @retry="retry"
    >
      <template v-if="dropped" #toolbar>
        <span class="text-xs text-muted-foreground">{{ t('sites.logs.dropped') }}</span>
      </template>
      <template #default="{ wrap }">
        <AccessLogTable v-if="kind === 'access'" :lines="lines" :wrap="wrap" />
        <LogText v-else :text="lines.join('\n')" :wrap="wrap" class="m-0 px-3 py-2 font-mono text-xs leading-relaxed" />
      </template>
    </LogView>
  </div>
</template>
