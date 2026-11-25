<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ArrowRight, Download, ExternalLink } from '@lucide/vue'
import { isAvailable, runtimePaths, type Offer, type SoftwareEntry } from '@/api/software'
import Card from '@/components/common/Card.vue'
import KeyValueList from '@/components/common/KeyValueList.vue'
import KeyValueRow from '@/components/common/KeyValueRow.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import { Button } from '@/components/ui/button'
import { useDrawerRoute } from '@/composables/useDrawerRoute'
import { formatIsoDate } from '@/i18n/format'
import { safeHttpsUrl } from '@/markdown/parse'
import {
  LINE_STATE_TONE,
  lineFacts,
  lineKey,
  lineState,
  lineStateText,
  methodText,
  supportState,
} from '@/software/display'
import { useSoftwareStore } from '@/stores/software'

// The line's exact version and where it comes from, then the other lines of
// the same software with Install (or Open, when installed too). Lines past
// their upstream support say so; installing one warns in the install sheet.
const props = defineProps<{ entry: SoftwareEntry; offer: Offer; canManage: boolean }>()

const { t } = useI18n()
const store = useSoftwareStore()
const drawer = useDrawerRoute('software')

const repo = computed(() => (props.offer.repo ? (store.repos.get(props.offer.repo) ?? null) : null))
const repoHome = computed(() => (repo.value ? safeHttpsUrl(repo.value.homepage) : null))
const paths = computed(() => runtimePaths(props.entry.id, props.offer))
const others = computed(() => props.entry.offers.filter((o) => o.line !== props.offer.line))
const busy = computed(() => store.runningFor(props.entry.id) !== null)
const standalone = computed(() => props.offer.method === 'standalone')

function openLine(line: string) {
  drawer.open(lineKey(props.entry.id, line), 'version')
}
</script>

<template>
  <Card :title="t('software.version.current', { line: offer.line })" :level="3" flush>
    <KeyValueList>
      <KeyValueRow v-if="offer.installedPackages?.length" :label="t('software.advanced.installedPackages')" mono>
        <span class="flex flex-col gap-0.5" dir="ltr">
          <span v-for="pkg in offer.installedPackages" :key="pkg.name">{{ pkg.name }} {{ pkg.version }}</span>
        </span>
      </KeyValueRow>
      <KeyValueRow v-else-if="offer.packages?.length" :label="standalone ? t('software.advanced.systemLibraries') : t('software.advanced.packages')" mono>
        <span dir="ltr">{{ offer.packages.join(', ') }}</span>
      </KeyValueRow>
      <KeyValueRow v-if="offer.method" :label="t('software.advanced.method')">
        {{ methodText(offer, store.repos) }}
        <a
          v-if="repoHome"
          :href="repoHome"
          target="_blank"
          rel="noopener noreferrer"
          class="inline-flex items-center gap-0.5 text-link underline-offset-2 hover:underline tap-y"
        >
          {{ t('software.homepage') }}
          <ExternalLink class="size-3" aria-hidden="true" />
        </a>
      </KeyValueRow>
      <KeyValueRow v-if="offer.installedPackages?.length && standalone && offer.packages?.length" :label="t('software.advanced.systemLibraries')" mono>
        <span dir="ltr">{{ offer.packages.join(', ') }}</span>
      </KeyValueRow>
      <KeyValueRow v-if="offer.stream" :label="t('software.advanced.stream')" mono>{{ offer.stream }}</KeyValueRow>
      <template v-if="paths">
        <KeyValueRow :label="t('software.advanced.location')" mono>{{ paths.line }}</KeyValueRow>
        <KeyValueRow :label="t('software.advanced.buildDirectory')" mono>{{ paths.build }}</KeyValueRow>
      </template>
      <KeyValueRow v-if="offer.services?.length" :label="t('software.advanced.services')" mono>{{ offer.services.join(', ') }}</KeyValueRow>
      <KeyValueRow v-if="offer.eol" :label="t('software.advanced.eol')">
        <StatusPill v-if="offer.endOfLife" tone="warning">{{ t('software.advanced.eolEnded', { date: formatIsoDate(offer.eol) }) }}</StatusPill>
        <template v-else-if="supportState(offer) === 'distribution'">
          {{ t('software.advanced.eolDistribution', { date: formatIsoDate(offer.eol) }) }}
        </template>
        <template v-else>{{ formatIsoDate(offer.eol) }}</template>
      </KeyValueRow>
    </KeyValueList>
    <p v-if="repo && !repo.official" class="m-0 border-t px-4 py-2.5 text-xs text-muted-foreground md:px-[18px]">
      {{ t('software.advanced.unofficialRepo', { repo: repo.name, software: entry.name }) }}
    </p>
  </Card>

  <Card v-if="others.length > 0" :title="t('software.version.others')" :level="3" flush>
    <ul class="m-0 list-none divide-y p-0">
      <li v-for="other in others" :key="other.line" class="flex flex-wrap items-center gap-x-3 gap-y-2 px-4 py-3 md:px-[18px]">
        <div class="flex min-w-0 flex-[1_1_240px] flex-col gap-0.5">
          <span class="flex flex-wrap items-center gap-2">
            <span class="font-mono font-medium" dir="ltr">{{ other.line }}</span>
            <StatusPill v-if="other.installed" :tone="LINE_STATE_TONE[lineState(other)]" dot>{{ lineStateText(lineState(other)) }}</StatusPill>
            <StatusPill v-if="other.endOfLife" tone="warning">{{ t('software.endOfLife.badge') }}</StatusPill>
          </span>
          <span class="text-xs text-muted-foreground">{{ lineFacts(other) }}</span>
          <span v-if="isAvailable(other) && !other.installed" class="text-xs text-muted-foreground">{{ methodText(other, store.repos) }}</span>
        </div>
        <Button v-if="other.installed" variant="outline" size="sm" class="max-md:h-11" @click="openLine(other.line)">
          {{ t('software.version.open') }}
          <ArrowRight class="rtl:rotate-180" aria-hidden="true" />
        </Button>
        <Button
          v-else-if="canManage && isAvailable(other)"
          variant="outline"
          size="sm"
          class="max-md:h-11"
          :disabled="busy"
          @click="store.openInstall(entry.id, other.line)"
        >
          <Download aria-hidden="true" />
          {{ t('software.action.install') }}
        </Button>
      </li>
    </ul>
  </Card>
</template>
