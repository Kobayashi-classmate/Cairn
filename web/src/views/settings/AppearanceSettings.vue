<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import Card from '@/components/common/Card.vue'
import LocaleSelect from '@/components/common/LocaleSelect.vue'
import ThemeSwitcher from '@/components/common/ThemeSwitcher.vue'
import { useSessionStore } from '@/stores/session'
import SettingRow from './SettingRow.vue'
import SettingRows from './SettingRows.vue'

// How the panel looks for you (interface spec, section 2): the colour scheme
// as cards with a preview, and the interface language. Both are saved to
// your account and apply at once.
const { t } = useI18n()
const session = useSessionStore()
const canSeePanel = computed(() => session.can('settings.read'))
</script>

<template>
  <Card :title="t('settings.appearance.theme')" :description="t('settings.appearance.themeHint')" :level="3">
    <ThemeSwitcher cards />
  </Card>

  <Card flush>
    <SettingRows>
      <SettingRow :title="t('settings.appearance.language')" label-for="appearance-language">
        <template #description>
          {{ t('settings.appearance.languageHint') }}
          <RouterLink
            v-if="canSeePanel"
            :to="{ name: 'settings-panel' }"
            class="text-link underline-offset-2 hover:underline"
          >
            {{ t('settings.appearance.defaultLanguageLink') }}
          </RouterLink>
        </template>
        <LocaleSelect id="appearance-language" class="w-full @lg:w-56" />
      </SettingRow>
    </SettingRows>
  </Card>
</template>
