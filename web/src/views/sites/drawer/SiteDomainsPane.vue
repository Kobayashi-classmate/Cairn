<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, toRef } from 'vue'
import { useI18n } from 'vue-i18n'
import { ArrowUp, Plus, X } from '@lucide/vue'
import type { CertView } from '@/api/certs'
import type { SiteView } from '@/api/sites'
import Card from '@/components/common/Card.vue'
import Notice from '@/components/common/Notice.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { domainsError } from '@/sites/form'
import { MAX_DOMAINS, normalizeHost } from '@/sites/validate'
import SaveBar from './SaveBar.vue'
import { useSitePane } from './useSitePane'

// The domains the site answers for. The first one is the primary domain
// (named in lists, opened by "Visit"); a certificate must cover all of them.
const props = defineProps<{
  site: SiteView
  canManage: boolean
  certs: readonly CertView[] | null
}>()

const { t } = useI18n()
const site = toRef(props, 'site')
const { form, errors, saving, error, dirty, busy, save, reset } = useSitePane(site, ['domains', 'tlsCert'], toRef(props, 'certs'))

const draft = ref('')
const draftError = ref('')

function add() {
  const host = normalizeHost(draft.value)
  if (!host) return
  if (form.domains.includes(host)) {
    draftError.value = t('sites.domains.duplicate', { domain: host })
    return
  }
  const message = domainsError([host])
  if (message) {
    draftError.value = message
    return
  }
  if (form.domains.length >= MAX_DOMAINS) {
    draftError.value = domainsError([...form.domains, host]) ?? ''
    return
  }
  form.domains = [...form.domains, host]
  draft.value = ''
  draftError.value = ''
}

function remove(domain: string) {
  form.domains = form.domains.filter((d) => d !== domain)
}

function makePrimary(domain: string) {
  form.domains = [domain, ...form.domains.filter((d) => d !== domain)]
}

const listError = computed(() => errors.value.domains ?? errors.value.tlsCert ?? '')
</script>

<template>
  <p class="m-0 text-muted-foreground">{{ t('sites.domains.intro') }}</p>

  <Card :level="3" flush>
    <ul class="m-0 list-none divide-y p-0" :aria-label="t('common.field.domains')">
      <li
        v-for="(domain, index) in form.domains"
        :key="domain"
        class="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-2.5 md:px-[18px]"
      >
        <span class="min-w-0 flex-1 truncate font-mono text-[13px] font-medium" dir="ltr">{{ domain }}</span>
        <StatusPill v-if="index === 0" tone="progress">{{ t('sites.domains.primary') }}</StatusPill>
        <template v-if="canManage">
          <Button
            v-if="index > 0"
            variant="ghost"
            size="sm"
            class="max-md:h-11"
            :disabled="busy"
            @click="makePrimary(domain)"
          >
            <ArrowUp aria-hidden="true" />
            {{ t('sites.domains.makePrimary') }}
          </Button>
          <Button
            variant="ghost"
            size="icon"
            class="text-destructive hover:text-destructive max-md:size-11"
            :disabled="busy || form.domains.length <= 1"
            :aria-label="t('sites.domains.remove', { domain })"
            :title="t('sites.domains.remove', { domain })"
            @click="remove(domain)"
          >
            <X aria-hidden="true" />
          </Button>
        </template>
      </li>
    </ul>
    <form
      v-if="canManage"
      class="flex flex-wrap items-start gap-2 border-t px-4 py-3 md:px-[18px]"
      novalidate
      @submit.prevent="add"
    >
      <div class="flex min-w-0 flex-[1_1_240px] flex-col gap-1">
        <Input
          v-model="draft"
          :aria-label="t('sites.domains.add')"
          :aria-invalid="draftError ? true : undefined"
          aria-describedby="site-domain-add-error"
          placeholder="www.example.com"
          autocomplete="off"
          autocapitalize="none"
          spellcheck="false"
          dir="ltr"
          class="font-mono"
          :disabled="busy"
          @input="draftError = ''"
        />
        <p v-if="draftError" id="site-domain-add-error" class="m-0 text-xs font-medium text-destructive">{{ draftError }}</p>
      </div>
      <Button type="submit" variant="outline" class="max-md:h-11" :disabled="busy || !draft.trim()">
        <Plus aria-hidden="true" />
        {{ t('sites.domains.add') }}
      </Button>
    </form>
  </Card>

  <Notice v-if="listError" tone="destructive">{{ listError }}</Notice>
  <p v-if="site.tls" class="m-0 text-detail text-muted-foreground">{{ t('sites.domains.certNote') }}</p>

  <SaveBar v-if="canManage" :dirty="dirty" :saving="saving" :applying="busy && !saving" :error="error" @save="save" @reset="reset" />
</template>
