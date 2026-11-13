<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, h, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router'
import {
  NButton,
  NIcon,
  NLayout,
  NLayoutContent,
  NLayoutHeader,
  NLayoutSider,
  NMenu,
  useMessage,
  type MenuOption,
} from 'naive-ui'
import { LogOutOutline, PersonCircleOutline } from '@vicons/ionicons5'
import ElevationBadge from '@/components/ElevationBadge.vue'
import LocaleSwitcher from '@/components/LocaleSwitcher.vue'
import { errorMessage } from '@/i18n/errors'
import { useSessionStore } from '@/stores/session'
import { homeLocation, visibleNavItems } from './navigation'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const session = useSessionStore()
const message = useMessage()

// Start collapsed on narrow screens.
const collapsed = ref(window.matchMedia('(max-width: 768px)').matches)
const signingOut = ref(false)

const home = computed(() => homeLocation(session.can))

const menuOptions = computed<MenuOption[]>(() =>
  visibleNavItems(session.can).map((item) => ({
    key: item.key,
    label: () => h(RouterLink, { to: item.to }, () => t(item.labelKey)),
    icon: () => h(NIcon, null, () => h(item.icon)),
  })),
)

const activeKey = computed(() => route.meta.navKey ?? null)

async function signOut() {
  signingOut.value = true
  try {
    await session.signOut()
    await router.replace({ name: 'login' })
  } catch (err) {
    message.error(errorMessage(err))
  } finally {
    signingOut.value = false
  }
}
</script>

<template>
  <NLayout class="shell">
    <NLayoutHeader bordered class="shell__header">
      <RouterLink :to="home" class="shell__brand">{{ t('common.app.name') }}</RouterLink>
      <div class="shell__actions">
        <ElevationBadge />
        <LocaleSwitcher />
        <RouterLink
          v-if="session.user"
          :to="{ name: 'settings-account' }"
          class="shell__user"
          :title="t('common.nav.account')"
        >
          <NIcon :size="18"><PersonCircleOutline /></NIcon>
          <span>{{ session.user.username }}</span>
        </RouterLink>
        <NButton quaternary size="small" :loading="signingOut" @click="signOut">
          <template #icon>
            <NIcon><LogOutOutline /></NIcon>
          </template>
          {{ t('common.action.signOut') }}
        </NButton>
      </div>
    </NLayoutHeader>
    <NLayout has-sider class="shell__body">
      <NLayoutSider
        v-model:collapsed="collapsed"
        bordered
        collapse-mode="width"
        :width="220"
        :collapsed-width="64"
        show-trigger="bar"
        :native-scrollbar="false"
      >
        <NMenu
          :value="activeKey"
          :options="menuOptions"
          :collapsed="collapsed"
          :collapsed-width="64"
          :collapsed-icon-size="22"
        />
      </NLayoutSider>
      <NLayoutContent :native-scrollbar="false" content-class="shell__content">
        <RouterView />
      </NLayoutContent>
    </NLayout>
  </NLayout>
</template>

<style scoped>
.shell {
  block-size: 100vh;
  block-size: 100dvh;
}

.shell__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  block-size: 56px;
  padding-inline: 16px;
}

.shell__brand {
  font-size: 18px;
  font-weight: 600;
  color: inherit;
  text-decoration: none;
}

.shell__actions {
  display: flex;
  align-items: center;
  gap: 8px;
}

.shell__user {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding-inline: 4px;
  color: inherit;
  text-decoration: none;
}

.shell__body {
  block-size: calc(100% - 56px);
}

.shell__body :deep(.shell__content) {
  padding: 24px;
}

@media (max-width: 640px) {
  .shell__user span {
    display: none;
  }

  .shell__body :deep(.shell__content) {
    padding: 16px;
  }
}
</style>
