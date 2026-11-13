<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { NCard, NH1, NText } from 'naive-ui'
import LocaleSwitcher from '@/components/LocaleSwitcher.vue'

withDefaults(
  defineProps<{
    title: string
    subtitle?: string
    /** Hide the corner switcher when the page has its own language field. */
    localeSwitcher?: boolean
  }>(),
  { subtitle: undefined, localeSwitcher: true },
)

const { t } = useI18n()
</script>

<template>
  <div class="auth">
    <div class="auth__toolbar">
      <LocaleSwitcher v-if="localeSwitcher" />
    </div>
    <main class="auth__main">
      <NCard class="auth__card" size="large">
        <div class="auth__brand">{{ t('common.app.name') }}</div>
        <NH1 class="auth__title">{{ title }}</NH1>
        <NText v-if="subtitle" depth="3" tag="p" class="auth__subtitle">{{ subtitle }}</NText>
        <slot />
      </NCard>
    </main>
  </div>
</template>

<style scoped>
.auth {
  display: flex;
  flex-direction: column;
  min-block-size: 100vh;
  min-block-size: 100dvh;
}

.auth__toolbar {
  display: flex;
  justify-content: flex-end;
  min-block-size: 48px;
  padding-block: 8px;
  padding-inline: 16px;
}

.auth__main {
  display: flex;
  flex: 1;
  align-items: flex-start;
  justify-content: center;
  padding-block: 4vh 48px;
  padding-inline: 16px;
}

.auth__card {
  inline-size: 100%;
  max-inline-size: 420px;
}

.auth__brand {
  font-size: 15px;
  font-weight: 600;
  letter-spacing: 0.02em;
  opacity: 0.7;
}

.auth__title {
  margin-block: 4px 4px;
  font-size: 24px;
}

.auth__subtitle {
  margin-block: 0 24px;
}
</style>
