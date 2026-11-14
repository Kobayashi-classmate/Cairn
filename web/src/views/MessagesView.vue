<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { nextTick, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import { NAlert, NButton, NCard, NEmpty, NH2, NIcon, NSpin, NTag, NText } from 'naive-ui'
import { OpenOutline } from '@vicons/ionicons5'
import type { Announcement } from '@/api/announcements'
import LevelTag from '@/components/LevelTag.vue'
import MarkdownText from '@/components/MarkdownText'
import { useErrorToast } from '@/composables/useErrorToast'
import { useExternalLink } from '@/composables/useExternalLink'
import { useNow } from '@/composables/useNow'
import { pickLocalized } from '@/i18n'
import { errorMessage } from '@/i18n/errors'
import { formatDateTime } from '@/i18n/format'
import { useAnnouncementsStore } from '@/stores/announcements'

const { t, te } = useI18n()
const route = useRoute()
const store = useAnnouncementsStore()
const showError = useErrorToast()
const openLink = useExternalLink()
const now = useNow(60_000)
const markingAll = ref(false)

onMounted(async () => {
  await store.refresh()
  await focusRequested()
})

watch(() => route.query.id, focusRequested)

/** Scrolls to the message named in `?id=` (opened from the bell). */
async function focusRequested() {
  const id = route.query.id
  if (typeof id !== 'string') return
  await nextTick()
  document.getElementById(`message-${id}`)?.scrollIntoView({ block: 'start', behavior: 'smooth' })
}

function categoryLabel(category: string): string {
  const key = `messages.category.${category}`
  return te(key) ? t(key) : category
}

function expired(item: Announcement): boolean {
  return item.expiresAt > 0 && item.expiresAt <= now.value
}

async function markRead(item: Announcement) {
  try {
    await store.markRead(item.id)
  } catch (err) {
    showError(err)
  }
}

async function markAll() {
  markingAll.value = true
  try {
    await store.markAllRead()
  } catch (err) {
    showError(err)
  } finally {
    markingAll.value = false
  }
}
</script>

<template>
  <div class="messages">
    <header class="messages__header">
      <NH2 class="messages__title">{{ t('messages.title') }}</NH2>
      <NButton :disabled="store.unread === 0" :loading="markingAll" @click="markAll">
        {{ t('messages.markAllRead') }}
      </NButton>
    </header>
    <NText depth="3" tag="p" class="messages__intro">{{ t('messages.intro') }}</NText>

    <NAlert v-if="store.error" type="error" class="messages__alert">
      {{ errorMessage(store.error) }}
    </NAlert>

    <NSpin :show="!store.loaded && !store.error">
      <NEmpty
        v-if="store.loaded && store.items.length === 0"
        :description="t('messages.empty')"
        class="messages__empty"
      />
      <div class="messages__list">
        <NCard
          v-for="item in store.items"
          :id="`message-${item.id}`"
          :key="item.id"
          size="small"
          :class="{ 'message--unread': !item.read }"
        >
          <template #header>
            <span class="message__title">{{ pickLocalized(item.title) }}</span>
          </template>
          <template #header-extra>
            <NTag v-if="!item.read" size="small" type="primary" :bordered="false">
              {{ t('messages.unread') }}
            </NTag>
          </template>
          <div class="message__meta">
            <LevelTag :level="item.level" />
            <NTag size="small" :bordered="false">{{ categoryLabel(item.category) }}</NTag>
            <NTag v-if="expired(item)" size="small" :bordered="false">{{ t('messages.expired') }}</NTag>
            <NText depth="3">{{ formatDateTime(item.publishedAt) }}</NText>
          </div>
          <MarkdownText :source="pickLocalized(item.body)" class="message__body" />
          <div class="message__actions">
            <NButton v-if="item.link" size="small" @click="openLink(item.link)">
              <template #icon>
                <NIcon><OpenOutline /></NIcon>
              </template>
              {{ t('messages.learnMore') }}
            </NButton>
            <NButton v-if="!item.read" size="small" quaternary @click="markRead(item)">
              {{ t('messages.markRead') }}
            </NButton>
          </div>
        </NCard>
      </div>
    </NSpin>
  </div>
</template>

<style scoped>
.messages {
  max-inline-size: 900px;
}

.messages__header {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px 16px;
}

.messages__title {
  margin: 0;
}

.messages__intro {
  margin-block: 4px 16px;
}

.messages__alert {
  margin-block-end: 16px;
}

.messages__empty {
  padding-block: 48px;
}

.messages__list {
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-block-size: 80px;
}

.message--unread {
  border-inline-start: 3px solid #f0a020;
}

.message__title {
  font-weight: 600;
}

.message__meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px 8px;
  margin-block-end: 12px;
  font-size: 13px;
}

.message__body {
  overflow-wrap: anywhere;
}

.message__actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-block-start: 12px;
}

.message__actions:empty {
  display: none;
}
</style>
