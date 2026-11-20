<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { ArrowRight } from '@lucide/vue'
import Notice from '@/components/common/Notice.vue'
import { useSessionStore } from '@/stores/session'

// Two-step verification reminder, shown on the overview only; elsewhere a dot
// on the avatar stands for it. Hiding it lasts for this browser session, so
// it returns in the next one until verification is turned on.
const HIDDEN_KEY = 'cairn.totpReminderHidden'

const { t } = useI18n()
const session = useSessionStore()

function readHidden(): boolean {
  try {
    return window.sessionStorage.getItem(HIDDEN_KEY) === '1'
  } catch {
    return false
  }
}

const hidden = ref(readHidden())
const show = computed(() => session.user !== null && !session.user.totpEnabled && !hidden.value)

function hide() {
  hidden.value = true
  try {
    window.sessionStorage.setItem(HIDDEN_KEY, '1')
  } catch {
    // Storage unavailable: hidden until the page is reloaded.
  }
}
</script>

<template>
  <Notice v-if="show" tone="warning" dismissible class="mb-5" @dismiss="hide">
    {{ t('common.totpReminder.notice') }}
    <template #action>
      <RouterLink
        :to="{ name: 'settings-security' }"
        class="inline-flex items-center gap-1 whitespace-nowrap text-current underline underline-offset-2 hover:no-underline"
      >
        {{ t('common.totpReminder.action') }}
        <ArrowRight class="size-3.5" aria-hidden="true" />
      </RouterLink>
    </template>
  </Notice>
</template>
