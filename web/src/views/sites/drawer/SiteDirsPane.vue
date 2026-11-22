<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, toRef } from 'vue'
import { useI18n } from 'vue-i18n'
import type { SiteView } from '@/api/sites'
import Card from '@/components/common/Card.vue'
import FormField from '@/components/common/FormField.vue'
import KeyValueList from '@/components/common/KeyValueList.vue'
import KeyValueRow from '@/components/common/KeyValueRow.vue'
import Notice from '@/components/common/Notice.vue'
import SwitchField from '@/components/common/SwitchField.vue'
import TagInput from '@/components/common/TagInput.vue'
import { Input } from '@/components/ui/input'
import { hasWritable, servesFiles, type SiteField } from '@/sites/form'
import { MAX_INDEX, MAX_WRITABLE, isIndexName, isRelativePath } from '@/sites/validate'
import SaveBar from './SaveBar.vue'
import { useSitePane } from './useSitePane'

// Where the site's files are and who may write them: web root and index
// files (static and PHP), the directories the runtime user may write, and
// whether links to files of other owners are served (static and PHP).
const props = defineProps<{ site: SiteView; canManage: boolean }>()

const { t } = useI18n()
const site = toRef(props, 'site')
const fields = computed<SiteField[]>(() => [
  ...(servesFiles(props.site.type) ? (['root', 'index'] as const) : []),
  ...(hasWritable(props.site.type) ? (['writable'] as const) : []),
])
const { form, errors, saving, error, dirty, busy, save, reset } = useSitePane(site, fields.value)

const disabled = computed(() => !props.canManage || busy.value)
const isWritable = (path: string) => path !== '' && isRelativePath(path)
</script>

<template>
  <Card :title="t('sites.dirs.location')" :level="3" flush>
    <KeyValueList>
      <KeyValueRow :label="t('sites.overview.dir')" mono><span dir="ltr">{{ site.dir }}</span></KeyValueRow>
      <KeyValueRow :label="t('sites.overview.owner')" mono><span dir="ltr">{{ site.owner }}</span></KeyValueRow>
      <KeyValueRow :label="t('sites.overview.runner')" mono><span dir="ltr">{{ site.runner }}</span></KeyValueRow>
    </KeyValueList>
  </Card>
  <p class="m-0 text-detail text-muted-foreground">{{ t('sites.overview.users', { owner: site.owner, runner: site.runner }) }}</p>

  <Card v-if="servesFiles(site.type) || hasWritable(site.type)" :title="t('sites.dirs.settings')" :level="3">
    <div class="flex flex-col gap-5">
      <template v-if="servesFiles(site.type)">
        <FormField id="site-dirs-root" :label="t('common.field.root')" :hint="t('sites.form.rootHint')" :error="errors.root">
          <template #default="field">
            <Input
              v-bind="field"
              v-model="form.root"
              :placeholder="t('sites.form.rootPlaceholder')"
              autocomplete="off"
              spellcheck="false"
              dir="ltr"
              class="font-mono"
              :disabled="disabled"
            />
          </template>
        </FormField>
        <FormField id="site-dirs-index" :label="t('common.field.index')" :hint="t('sites.form.indexHint')" :error="errors.index">
          <template #default="field">
            <TagInput
              v-bind="field"
              v-model="form.index"
              :valid="isIndexName"
              :max="MAX_INDEX"
              mono
              placeholder="index.html"
              :disabled="disabled"
            />
          </template>
        </FormField>
        <SwitchField
          id="site-dirs-symlinks"
          v-model="form.allowSymlinks"
          :label="t('sites.dirs.allowSymlinks')"
          :hint="t('sites.dirs.allowSymlinksHint')"
          :disabled="disabled"
        />
        <Notice v-if="form.allowSymlinks" tone="warning">{{ t('sites.dirs.allowSymlinksRisk') }}</Notice>
      </template>
      <FormField
        v-if="hasWritable(site.type)"
        id="site-dirs-writable"
        :label="t('common.field.writable')"
        :hint="site.type === 'app' ? t('sites.app.writableHint') : t('sites.form.writableHint')"
        :error="errors.writable"
      >
        <template #default="field">
          <TagInput
            v-bind="field"
            v-model="form.writable"
            :valid="isWritable"
            :max="MAX_WRITABLE"
            mono
            placeholder="uploads"
            :disabled="disabled"
          />
        </template>
      </FormField>
    </div>
  </Card>

  <SaveBar
    v-if="canManage && (servesFiles(site.type) || hasWritable(site.type))"
    :dirty="dirty"
    :saving="saving"
    :applying="busy && !saving"
    :error="error"
    @save="save"
    @reset="reset"
  />
</template>
