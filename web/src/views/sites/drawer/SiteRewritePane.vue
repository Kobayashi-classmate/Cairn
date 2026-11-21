<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, toRef } from 'vue'
import { useI18n } from 'vue-i18n'
import type { RewriteRule, SiteView } from '@/api/sites'
import Card from '@/components/common/Card.vue'
import OptionGroup from '@/components/common/OptionGroup.vue'
import type { OptionItem } from '@/components/common/types'
import { rewriteLabel } from '@/sites/labels'
import SaveBar from './SaveBar.vue'
import { useSitePane } from './useSitePane'

// Rewrite rules of static and PHP sites, from the built-in set.
const props = defineProps<{
  site: SiteView
  canManage: boolean
  rewrites: readonly RewriteRule[]
}>()

const { t } = useI18n()
const site = toRef(props, 'site')
const { form, saving, error, dirty, busy, save, reset } = useSitePane(site, [])

const options = computed<OptionItem[]>(() => {
  const list: OptionItem[] = [
    { value: '', label: t('sites.form.noRewrite'), hint: t('sites.rewrite.noneHint') },
    ...props.rewrites
      .filter((rule) => rule.types.includes(props.site.type))
      .map((rule) => ({ value: rule.id, label: rewriteLabel(rule) })),
  ]
  if (props.site.rewrite && !list.some((option) => option.value === props.site.rewrite)) {
    list.push({ value: props.site.rewrite, label: props.site.rewrite })
  }
  return list
})
</script>

<template>
  <p class="m-0 text-muted-foreground">{{ t('sites.rewrite.intro') }}</p>
  <Card :title="t('sites.rewrite.rules')" :level="3">
    <OptionGroup v-model="form.rewrite" :options="options" :label="t('sites.rewrite.rules')" :disabled="!canManage || busy" grid />
  </Card>
  <SaveBar v-if="canManage" :dirty="dirty" :saving="saving" :applying="busy && !saving" :error="error" @save="save" @reset="reset" />
</template>
