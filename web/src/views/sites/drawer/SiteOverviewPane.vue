<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { requestOf, type RewriteRule, type SiteView } from '@/api/sites'
import Card from '@/components/common/Card.vue'
import KeyValueList from '@/components/common/KeyValueList.vue'
import KeyValueRow from '@/components/common/KeyValueRow.vue'
import Notice from '@/components/common/Notice.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import { useClock } from '@/composables/useClock'
import { errorText } from '@/i18n/errors'
import { formatDateTime } from '@/i18n/format'
import { notifyError } from '@/lib/notify'
import { rewriteLabel, siteDisplayState } from '@/sites/labels'
import {
  SITE_STATE_TONE,
  appRuntimeLabel,
  backupStatus,
  httpsStatus,
  siteStateText,
  siteTypeText,
} from '@/sites/display'
import { saveSite } from '../actions'

// The site at a glance: state, domains, type, directory and users, when it
// changed, HTTPS and backups, with Start / Stop.
const props = defineProps<{
  site: SiteView
  canManage: boolean
  backupsKnown: boolean
  rewrites: readonly RewriteRule[]
}>()

const emit = defineEmits<{ go: [section: string] }>()

const { t } = useI18n()
const now = useClock()
const reapplying = ref(false)

const state = computed(() => siteDisplayState(props.site))
const https = computed(() => httpsStatus(props.site, now.value))
const backup = computed(() => backupStatus(props.site.backup, props.backupsKnown))
const failure = computed(() => (props.site.errorCode ? (errorText(props.site.errorCode) ?? props.site.errorCode) : ''))

const rewriteName = computed(() => {
  const id = props.site.rewrite
  if (!id) return t('sites.form.noRewrite')
  const rule = props.rewrites.find((r) => r.id === id)
  return rule ? rewriteLabel(rule) : id
})

/** Applies the stored settings again, for example after a failed change. */
async function reapply() {
  reapplying.value = true
  try {
    await saveSite(props.site, requestOf(props.site))
  } catch (err) {
    notifyError(err)
  } finally {
    reapplying.value = false
  }
}
</script>

<template>
  <Notice v-if="site.state === 'failed' || site.drift" :tone="site.state === 'failed' ? 'destructive' : 'warning'">
    {{ t('sites.drift.title') }}: {{ failure || t('sites.drift.text') }}
    <template #action>
      <button
        v-if="canManage"
        type="button"
        class="underline underline-offset-2 hover:no-underline disabled:opacity-50"
        :disabled="reapplying || site.state === 'applying'"
        @click="reapply"
      >
        {{ t('sites.drift.reapply') }}
      </button>
      <RouterLink
        v-if="site.lastJob"
        :to="{ name: 'job', params: { id: site.lastJob } }"
        class="text-current underline underline-offset-2 hover:no-underline"
      >
        {{ t('sites.lastChange') }}
      </RouterLink>
    </template>
  </Notice>

  <Card :title="t('sites.overview.basics')" :level="3" flush>
    <KeyValueList>
      <KeyValueRow :label="t('sites.list.state')">
        <StatusDot :tone="SITE_STATE_TONE[state]">{{ siteStateText(site) }}</StatusDot>
      </KeyValueRow>
      <KeyValueRow :label="t('common.field.domains')" mono>
        <span v-for="domain in site.domains" :key="domain" dir="ltr">{{ domain }}</span>
      </KeyValueRow>
      <KeyValueRow :label="t('common.field.type')">{{ siteTypeText(site) }}</KeyValueRow>
      <KeyValueRow :label="t('sites.overview.dir')" mono><span dir="ltr">{{ site.dir }}</span></KeyValueRow>
      <KeyValueRow :label="t('sites.overview.owner')" mono><span dir="ltr">{{ site.owner }}</span></KeyValueRow>
      <KeyValueRow :label="t('sites.overview.runner')" mono><span dir="ltr">{{ site.runner }}</span></KeyValueRow>
      <KeyValueRow :label="t('sites.overview.created')">{{ formatDateTime(site.createdAt) }}</KeyValueRow>
      <KeyValueRow :label="t('sites.overview.updated')">
        <RelativeTime :value="site.updatedAt" />
        <RouterLink
          v-if="site.lastJob"
          :to="{ name: 'job', params: { id: site.lastJob } }"
          class="text-detail text-link underline-offset-2 hover:underline tap-y"
        >
          {{ t('sites.overview.viewChange') }}
        </RouterLink>
      </KeyValueRow>
    </KeyValueList>
  </Card>

  <Card :title="t('sites.overview.status')" :level="3" flush>
    <KeyValueList>
      <KeyValueRow :label="t('sites.form.https')">
        <StatusPill :tone="https.tone">{{ https.text }}</StatusPill>
        <span v-if="site.cert" class="text-detail text-muted-foreground">{{ site.cert.name }}</span>
        <button type="button" class="text-detail text-link underline-offset-2 hover:underline tap-y" @click="emit('go', 'https')">
          {{ t('sites.overview.manage') }}
        </button>
      </KeyValueRow>
      <KeyValueRow :label="t('sites.list.backup')">
        <span v-if="!backup" class="text-muted-foreground">—</span>
        <template v-else>
          <StatusPill v-if="backup.text" :tone="backup.tone">{{ backup.text }}</StatusPill>
          <RelativeTime v-if="backup.at" :value="backup.at" />
        </template>
        <button type="button" class="text-detail text-link underline-offset-2 hover:underline tap-y" @click="emit('go', 'backup')">
          {{ t('sites.overview.manage') }}
        </button>
      </KeyValueRow>
      <KeyValueRow v-if="site.type === 'php' || site.type === 'static'" :label="t('common.field.rewrite')">
        {{ rewriteName }}
      </KeyValueRow>
      <KeyValueRow v-if="site.type === 'proxy' && site.proxy" :label="t('common.field.proxy')" mono>
        <span dir="ltr">{{ site.proxy.url }}</span>
      </KeyValueRow>
      <KeyValueRow v-if="site.type === 'app' && site.app" :label="t('sites.tabs.app')">
        <span>{{ appRuntimeLabel(site) ?? t('sites.app.systemTools') }}</span>
        <span class="font-mono text-[12.5px]" dir="ltr">127.0.0.1:{{ site.app.port }}</span>
      </KeyValueRow>
    </KeyValueList>
  </Card>

  <p class="m-0 text-detail text-muted-foreground">
    {{ t('sites.overview.users', { owner: site.owner, runner: site.runner }) }}
  </p>

</template>
