<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, h, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router'
import {
  NAlert,
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
import type { Announcement } from '@/api/announcements'
import ElevationBadge from '@/components/ElevationBadge.vue'
import LocaleSwitcher from '@/components/LocaleSwitcher.vue'
import MessageBell from '@/components/MessageBell.vue'
import { useAnnouncementPolling } from '@/composables/useAnnouncementPolling'
import { pickLocalized } from '@/i18n'
import { errorMessage } from '@/i18n/errors'
import { useAnnouncementsStore } from '@/stores/announcements'
import { useSessionStore } from '@/stores/session'
import { useNavStore } from '@/stores/nav'
import { homeLocation, resolveNav } from './navigation'

const { t, te } = useI18n()
const route = useRoute()
const router = useRouter()
const session = useSessionStore()
const announcements = useAnnouncementsStore()
const message = useMessage()

useAnnouncementPolling()

// Two-step verification reminder: it can be hidden for this browser session
// only, so it returns in the next one until the user turns it on.
const TOTP_REMINDER_KEY = 'cairn.totpReminderHidden'

function readReminderHidden(): boolean {
  try {
    return window.sessionStorage.getItem(TOTP_REMINDER_KEY) === '1'
  } catch {
    return false
  }
}

const totpReminderHidden = ref(readReminderHidden())
const showTotpReminder = computed(
  () => session.user !== null && !session.user.totpEnabled && !totpReminderHidden.value,
)

function hideTotpReminder() {
  totpReminderHidden.value = true
  try {
    window.sessionStorage.setItem(TOTP_REMINDER_KEY, '1')
  } catch {
    // Storage unavailable: hidden until the page is reloaded.
  }
}

// Start collapsed on narrow screens.
const collapsed = ref(window.matchMedia('(max-width: 768px)').matches)
const signingOut = ref(false)

const nav = useNavStore()
const home = computed(() => homeLocation(router))

const menuOptions = computed<MenuOption[]>(() =>
  resolveNav(nav.entries, router).map((item) => ({
    key: item.key,
    // A label key this build lacks shows the entry's key rather than the raw i18n key.
    label: () => h(RouterLink, { to: item.to }, () => (te(item.labelKey) ? t(item.labelKey) : item.key)),
    icon: () => h(NIcon, null, () => h(item.icon)),
  })),
)

const activeKey = computed(() => route.meta.navKey ?? null)

async function markBannerRead(item: Announcement) {
  try {
    await announcements.markRead(item.id)
  } catch (err) {
    message.error(errorMessage(err))
  }
}

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
        <MessageBell />
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
        <!-- Unread critical official messages stay visible until read or dismissed. -->
        <NAlert
          v-for="item in announcements.banners"
          :key="item.id"
          type="error"
          closable
          :title="pickLocalized(item.title)"
          class="shell__banner"
          @close="announcements.dismiss(item.id)"
        >
          <div class="shell__banner-actions">
            <RouterLink :to="{ name: 'messages', query: { id: item.id } }">
              {{ t('messages.view') }}
            </RouterLink>
            <NButton text size="small" @click="markBannerRead(item)">
              {{ t('messages.markRead') }}
            </NButton>
          </div>
        </NAlert>
        <NAlert
          v-if="showTotpReminder"
          type="warning"
          closable
          :title="t('common.totpReminder.title')"
          class="shell__banner"
          @close="hideTotpReminder"
        >
          <div class="shell__banner-actions">
            <span>{{ t('common.totpReminder.text') }}</span>
            <RouterLink :to="{ name: 'settings-security' }">{{ t('common.totpReminder.action') }}</RouterLink>
          </div>
        </NAlert>
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

.shell__banner {
  margin-block-end: 16px;
}

.shell__banner-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 16px;
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
