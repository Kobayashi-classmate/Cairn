<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { toRef } from 'vue'
import { useI18n } from 'vue-i18n'
import type { SiteView } from '@/api/sites'
import Card from '@/components/common/Card.vue'
import FormField from '@/components/common/FormField.vue'
import SwitchField from '@/components/common/SwitchField.vue'
import { Input } from '@/components/ui/input'
import SaveBar from './SaveBar.vue'
import { useSitePane } from './useSitePane'

// Where a reverse proxy site forwards requests.

/** Example upstream; kept out of the template (see SiteCreateSheet). */
const PROXY_EXAMPLE = 'http://127.0.0.1:3000'
const props = defineProps<{ site: SiteView; canManage: boolean }>()

const { t } = useI18n()
const site = toRef(props, 'site')
const { form, errors, saving, error, dirty, busy, save, reset } = useSitePane(site, ['proxyUrl'])
</script>

<template>
  <Card :title="t('sites.proxy.upstream')" :level="3">
    <div class="flex flex-col gap-5">
      <FormField id="site-proxy-url" :label="t('common.field.proxy')" :hint="t('sites.form.proxyHint')" :error="errors.proxyUrl">
        <template #default="field">
          <Input
            v-bind="field"
            v-model="form.proxyUrl"
            type="url"
            :placeholder="PROXY_EXAMPLE"
            autocomplete="off"
            spellcheck="false"
            dir="ltr"
            class="font-mono"
            :disabled="!canManage || busy"
          />
        </template>
      </FormField>
      <SwitchField
        v-model="form.webSocket"
        :label="t('sites.form.webSocket')"
        :hint="t('sites.form.webSocketHint')"
        :disabled="!canManage || busy"
      />
      <SwitchField
        v-model="form.upstreamHost"
        :label="t('sites.form.upstreamHost')"
        :hint="t('sites.form.upstreamHostHint')"
        :disabled="!canManage || busy"
      />
    </div>
  </Card>
  <SaveBar v-if="canManage" :dirty="dirty" :saving="saving" :applying="busy && !saving" :error="error" @save="save" @reset="reset" />
</template>
