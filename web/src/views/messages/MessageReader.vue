<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Check, ExternalLink, LoaderCircle } from '@lucide/vue'
import type { Announcement } from '@/api/announcements'
import RelativeTime from '@/components/common/RelativeTime.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import MarkdownText from '@/components/MarkdownText'
import { Button } from '@/components/ui/button'
import { useClock } from '@/composables/useClock'
import { useExternalLink } from '@/composables/useExternalLink'
import { pickLocalized } from '@/i18n'
import { notifyError } from '@/lib/notify'
import { useAnnouncementsStore } from '@/stores/announcements'
import { levelText, levelTone } from '@/views/notify/labels'

// One official message: its title, level, category and date, the text
// (limited Markdown), a link to more information and "Mark as read".
const props = defineProps<{ message: Announcement }>()

const { t, te } = useI18n()
const store = useAnnouncementsStore()
const openLink = useExternalLink()
const now = useClock()
const marking = ref(false)

const expired = computed(() => props.message.expiresAt > 0 && props.message.expiresAt <= now.value)

function categoryLabel(category: string): string {
  const key = `messages.category.${category}`
  return te(key) ? t(key) : category
}

async function markRead() {
  marking.value = true
  try {
    await store.markRead(props.message.id)
  } catch (err) {
    notifyError(err)
  } finally {
    marking.value = false
  }
}
</script>

<template>
  <article class="flex min-w-0 flex-col gap-4">
    <header class="flex flex-col gap-2">
      <h2 class="m-0 text-title [overflow-wrap:anywhere]">{{ pickLocalized(message.title) }}</h2>
      <div class="flex flex-wrap items-center gap-2 text-detail text-muted-foreground">
        <StatusPill :tone="levelTone(message.level)">{{ levelText(message.level) }}</StatusPill>
        <StatusPill>{{ categoryLabel(message.category) }}</StatusPill>
        <StatusPill v-if="expired">{{ t('messages.expired') }}</StatusPill>
        <span>{{ t('messages.published') }} <RelativeTime :value="message.publishedAt" /></span>
      </div>
    </header>
    <MarkdownText :source="pickLocalized(message.body)" class="[overflow-wrap:anywhere]" />
    <footer class="flex flex-wrap items-center gap-2 border-t pt-4">
      <Button v-if="message.link" variant="outline" class="max-md:h-11" @click="openLink(message.link)">
        <ExternalLink aria-hidden="true" />
        {{ t('messages.learnMore') }}
      </Button>
      <Button v-if="!message.read" class="max-md:h-11" :disabled="marking" @click="markRead">
        <LoaderCircle v-if="marking" class="animate-spin" aria-hidden="true" />
        <Check v-else aria-hidden="true" />
        {{ t('messages.markRead') }}
      </Button>
      <span v-else class="inline-flex items-center gap-1.5 text-detail text-muted-foreground" role="status">
        <Check class="size-4" aria-hidden="true" />
        {{ t('messages.read') }}
      </span>
    </footer>
  </article>
</template>
