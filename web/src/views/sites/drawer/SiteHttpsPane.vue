<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, toRef } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { Plus } from '@lucide/vue'
import type { CertView } from '@/api/certs'
import type { SiteView } from '@/api/sites'
import Card from '@/components/common/Card.vue'
import FormField from '@/components/common/FormField.vue'
import KeyValueList from '@/components/common/KeyValueList.vue'
import KeyValueRow from '@/components/common/KeyValueRow.vue'
import Notice from '@/components/common/Notice.vue'
import SelectField from '@/components/common/SelectField.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import SwitchField from '@/components/common/SwitchField.vue'
import type { SelectOption } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { useClock } from '@/composables/useClock'
import { useNavRoutes } from '@/composables/useNavRoutes'
import { formatDate } from '@/i18n/format'
import { coveringCerts } from '@/sites/form'
import { certSummaryStatus } from '@/sites/display'
import { useSessionStore } from '@/stores/session'
import SaveBar from './SaveBar.vue'
import { useSitePane } from './useSitePane'

// HTTPS of the site: a stored certificate that covers every domain, the
// redirect from HTTP and HSTS. Certificates are issued on the certificates
// page.
const props = defineProps<{
  site: SiteView
  canManage: boolean
  /** Valid certificates; null when the user cannot see certificates. */
  certs: readonly CertView[] | null
}>()

const { t } = useI18n()
const now = useClock()
const session = useSessionStore()
const navRoutes = useNavRoutes()
const site = toRef(props, 'site')
const { form, errors, saving, error, dirty, busy, save, reset } = useSitePane(site, ['tlsCert'], toRef(props, 'certs'))

const covering = computed(() => coveringCerts(props.certs, form.domains))

const options = computed<SelectOption[]>(() => {
  const list: SelectOption[] = [
    { value: '', label: t('sites.form.noTls') },
    ...covering.value.map((cert) => ({ value: cert.id, label: `${cert.name} (${cert.domains.join(', ')})` })),
  ]
  // The certificate in use stays selectable even when it is not offered (expired, not visible).
  const current = props.site.tls?.cert
  if (current && !list.some((option) => option.value === current)) {
    list.push({ value: current, label: t('sites.https.current', { name: props.site.cert?.name ?? current }) })
  }
  return list
})

const certStatus = computed(() => (props.site.cert ? certSummaryStatus(props.site.cert, now.value) : null))
const canIssue = computed(() => navRoutes.value.has('certs') && session.can('certs.manage'))
const certLink = computed(() =>
  props.site.tls && navRoutes.value.has('certs') ? { name: 'certs', query: { open: `cert:${props.site.tls.cert}` } } : null,
)
/** The issue sheet of the certificates page, filled in with the site's names (IP addresses cannot be in one). */
const issueLink = computed(() => ({
  name: 'certs',
  query: { issue: props.site.domains.filter((domain) => !/^\d+(\.\d+){3}$/.test(domain)).join(',') },
}))
</script>

<template>
  <Card v-if="site.tls" :title="t('sites.https.certificate')" :level="3" flush>
    <template v-if="certLink" #actions>
      <RouterLink :to="certLink" class="text-detail font-medium text-link underline-offset-2 hover:underline">
        {{ t('sites.https.openCert') }}
      </RouterLink>
    </template>
    <KeyValueList>
      <KeyValueRow :label="t('certs.column.state')">
        <StatusPill v-if="certStatus" :tone="certStatus.tone">{{ certStatus.text }}</StatusPill>
        <span v-else class="text-muted-foreground">{{ t('sites.https.on') }}</span>
      </KeyValueRow>
      <KeyValueRow :label="t('common.field.name')">{{ site.cert?.name ?? site.tls.cert }}</KeyValueRow>
      <KeyValueRow v-if="site.cert?.notAfter" :label="t('certs.column.expires')">
        {{ formatDate(site.cert.notAfter) }}
      </KeyValueRow>
      <KeyValueRow v-if="site.cert" :label="t('certs.column.autoRenew')">
        <StatusPill :tone="site.cert.autoRenew ? 'success' : 'neutral'">
          {{ site.cert.autoRenew ? t('common.status.on') : t('common.status.off') }}
        </StatusPill>
      </KeyValueRow>
    </KeyValueList>
  </Card>
  <Notice v-else tone="warning">{{ t('sites.https.offNotice') }}</Notice>

  <Card :title="t('sites.https.settings')" :level="3">
    <div class="flex flex-col gap-5">
      <FormField
        id="site-https-cert"
        :label="t('sites.form.certificate')"
        :hint="certs ? t('sites.form.certHint') : t('sites.https.noCertAccess')"
        :error="errors.tlsCert"
      >
        <template #default="field">
          <SelectField v-bind="field" v-model="form.tlsCert" :options="options" :disabled="!canManage || busy || !certs" />
        </template>
      </FormField>
      <template v-if="form.tlsCert">
        <SwitchField
          v-model="form.tlsRedirect"
          :label="t('sites.form.redirect')"
          :hint="t('sites.form.redirectHint')"
          :disabled="!canManage || busy"
        />
        <SwitchField
          v-model="form.tlsHsts"
          :label="t('sites.form.hsts')"
          :hint="t('sites.form.hstsHint')"
          :disabled="!canManage || busy"
        />
      </template>
      <div v-if="canIssue && covering.length === 0" class="flex flex-col items-start gap-2">
        <p class="m-0 text-detail text-muted-foreground">{{ t('sites.https.noCovering') }}</p>
        <Button as-child variant="outline" size="sm">
          <RouterLink :to="issueLink">
            <Plus aria-hidden="true" />
            {{ t('sites.https.issue') }}
          </RouterLink>
        </Button>
      </div>
      <RouterLink
        v-else-if="canIssue"
        :to="issueLink"
        class="self-start text-detail text-link underline-offset-2 hover:underline"
      >
        {{ t('sites.https.issueAnother') }}
      </RouterLink>
    </div>
  </Card>

  <SaveBar v-if="canManage" :dirty="dirty" :saving="saving" :applying="busy && !saving" :error="error" @save="save" @reset="reset" />
</template>
