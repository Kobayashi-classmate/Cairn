<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, useRouter } from 'vue-router'
import { NBadge, NButton, NEmpty, NIcon, NPopover, NText } from 'naive-ui'
import { NotificationsOutline } from '@vicons/ionicons5'
import type { Announcement } from '@/api/announcements'
import LevelTag from '@/components/LevelTag.vue'
import { useErrorToast } from '@/composables/useErrorToast'
import { pickLocalized } from '@/i18n'
import { formatDateTime } from '@/i18n/format'
import { useAnnouncementsStore } from '@/stores/announcements'

const LATEST_COUNT = 5

const { t } = useI18n()
const router = useRouter()
const store = useAnnouncementsStore()
const showError = useErrorToast()
const open = ref(false)

const latest = computed(() => store.items.slice(0, LATEST_COUNT))

async function openItem(item: Announcement) {
  open.value = false
  await router.push({ name: 'messages', query: { id: item.id } })
  try {
    await store.markRead(item.id)
  } catch (err) {
    showError(err)
  }
}

async function markAll() {
  try {
    await store.markAllRead()
  } catch (err) {
    showError(err)
  }
}
</script>

<template>
  <NPopover v-model:show="open" trigger="click" placement="bottom-end" :show-arrow="false">
    <template #trigger>
      <NBadge :value="store.unread" :max="99" :show="store.unread > 0" :offset="[-4, 4]">
        <NButton
          quaternary
          circle
          size="small"
          :aria-label="t('messages.bell', { count: store.unread }, store.unread)"
        >
          <template #icon>
            <NIcon><NotificationsOutline /></NIcon>
          </template>
        </NButton>
      </NBadge>
    </template>
    <div class="bell">
      <div class="bell__head">
        <NText strong>{{ t('messages.title') }}</NText>
        <NButton text size="small" type="primary" :disabled="store.unread === 0" @click="markAll">
          {{ t('messages.markAllRead') }}
        </NButton>
      </div>
      <NEmpty v-if="latest.length === 0" size="small" :description="t('messages.empty')" class="bell__empty" />
      <ul v-else class="bell__list">
        <li v-for="item in latest" :key="item.id">
          <button type="button" class="bell__item" @click="openItem(item)">
            <span class="bell__line">
              <span v-if="!item.read" class="bell__dot" :aria-label="t('messages.unread')" />
              <span class="bell__title" :class="{ 'bell__title--unread': !item.read }">
                {{ pickLocalized(item.title) }}
              </span>
            </span>
            <span class="bell__meta">
              <LevelTag :level="item.level" />
              <NText depth="3">{{ formatDateTime(item.publishedAt) }}</NText>
            </span>
          </button>
        </li>
      </ul>
      <div class="bell__foot">
        <RouterLink :to="{ name: 'messages' }" class="bell__all" @click="open = false">
          {{ t('messages.viewAll') }}
        </RouterLink>
      </div>
    </div>
  </NPopover>
</template>

<style scoped>
.bell {
  inline-size: min(340px, calc(100vw - 48px));
}

.bell__head,
.bell__foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding-block: 4px;
}

.bell__empty {
  padding-block: 16px;
}

.bell__list {
  margin: 0;
  padding: 0;
  list-style: none;
}

.bell__item {
  display: flex;
  flex-direction: column;
  gap: 4px;
  inline-size: 100%;
  padding: 8px;
  border: 0;
  border-radius: 4px;
  background: none;
  color: inherit;
  font: inherit;
  text-align: start;
  cursor: pointer;
}

.bell__item:hover,
.bell__item:focus-visible {
  background: rgb(128 128 128 / 12%);
}

.bell__line {
  display: flex;
  align-items: center;
  gap: 6px;
}

.bell__dot {
  flex: none;
  inline-size: 8px;
  block-size: 8px;
  border-radius: 50%;
  background: #f0a020;
}

.bell__title {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.bell__title--unread {
  font-weight: 600;
}

.bell__meta {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
}

.bell__all {
  color: inherit;
}
</style>
