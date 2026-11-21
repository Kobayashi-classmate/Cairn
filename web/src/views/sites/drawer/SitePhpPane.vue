<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, toRef } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import type { SiteView } from '@/api/sites'
import Card from '@/components/common/Card.vue'
import Notice from '@/components/common/Notice.vue'
import OptionGroup from '@/components/common/OptionGroup.vue'
import type { OptionItem } from '@/components/common/types'
import { useNavRoutes } from '@/composables/useNavRoutes'
import SaveBar from './SaveBar.vue'
import { useSitePane } from './useSitePane'

// The PHP version of a PHP site; each version runs its own PHP-FPM pool.
const props = defineProps<{
  site: SiteView
  canManage: boolean
  /** Installed PHP lines with PHP-FPM. */
  phpLines: readonly string[]
}>()

const { t } = useI18n()
const navRoutes = useNavRoutes()
const site = toRef(props, 'site')
const { form, errors, saving, error, dirty, busy, save, reset } = useSitePane(site, ['php'])

const options = computed<OptionItem[]>(() => {
  const list: OptionItem[] = props.phpLines.map((line) => ({
    value: line,
    label: `PHP ${line}`,
    hint: line === props.site.php ? t('sites.php.current') : undefined,
  }))
  // The version in use stays visible after it was removed.
  if (props.site.php && !props.phpLines.includes(props.site.php)) {
    list.push({ value: props.site.php, label: `PHP ${props.site.php}`, hint: t('sites.php.notInstalled'), disabled: true })
  }
  return list
})
</script>

<template>
  <p class="m-0 text-muted-foreground">{{ t('sites.php.intro', { runner: site.runner }) }}</p>
  <Notice v-if="phpLines.length === 0" tone="warning">
    {{ t('sites.form.noPhp') }}
    <template v-if="navRoutes.has('software')" #action>
      <RouterLink :to="{ name: 'software' }" class="text-current underline underline-offset-2">{{ t('sites.form.installPhp') }}</RouterLink>
    </template>
  </Notice>
  <Card :title="t('sites.php.version')" :level="3">
    <OptionGroup v-model="form.php" :options="options" :label="t('sites.php.version')" :disabled="!canManage || busy" grid />
    <p v-if="errors.php" class="m-0 mt-2 text-xs font-medium text-destructive">{{ errors.php }}</p>
  </Card>
  <SaveBar v-if="canManage" :dirty="dirty" :saving="saving" :applying="busy && !saving" :error="error" @save="save" @reset="reset" />
</template>
