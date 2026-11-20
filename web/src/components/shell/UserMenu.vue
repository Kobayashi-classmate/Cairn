<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { Languages, LogOut, Megaphone, ShieldAlert, UserRound } from '@lucide/vue'
import type { AcceptableValue } from 'reka-ui'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { roleLabel } from '@/auth/permissions'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { useLocaleChoice } from '@/composables/useLocaleChoice'
import { useSignOut } from '@/composables/useSignOut'
import { cn } from '@/lib/utils'
import { useAnnouncementsStore } from '@/stores/announcements'
import { useSessionStore } from '@/stores/session'

// The account row at the bottom of the sidebar and its menu: account
// settings, messages, language and sign out. A dot on the avatar means
// two-step verification is off.
const props = defineProps<{
  /** Icon rail: show only the avatar. */
  compact?: boolean
  /** Whether the menu from the server includes the messages page. */
  hasMessages?: boolean
}>()

const emit = defineEmits<{ navigate: [] }>()

const { t } = useI18n()
const session = useSessionStore()
const announcements = useAnnouncementsStore()
const { locale, choices, switching, select } = useLocaleChoice()
const { signingOut, signOut } = useSignOut()

const user = computed(() => session.user)
const initial = computed(() => (user.value?.username.charAt(0) ?? '?').toUpperCase())
const role = computed(() => (user.value ? roleLabel(user.value.role) : ''))
const totpOff = computed(() => user.value !== null && !user.value.totpEnabled)

const triggerLabel = computed(() => {
  const name = user.value?.username ?? ''
  return totpOff.value ? t('shell.userMenuTotpOff', { name }) : t('shell.userMenu', { name })
})

function onLocale(value: AcceptableValue) {
  if (typeof value === 'string') void select(value)
}
</script>

<template>
  <DropdownMenu v-if="user">
    <DropdownMenuTrigger
      :aria-label="triggerLabel"
      :class="
        cn(
          'flex min-w-0 items-center gap-2.5 rounded-md text-start outline-none hover:bg-sidebar-accent focus-visible:ring-2 focus-visible:ring-ring/50 data-[state=open]:bg-sidebar-accent',
          props.compact ? 'size-10 justify-center' : 'flex-1 px-1.5 py-1.5 max-md:py-2',
        )
      "
    >
      <span class="relative shrink-0">
        <Avatar class="size-[30px]" aria-hidden="true">
          <AvatarFallback class="bg-primary-soft text-detail font-semibold text-primary-soft-foreground">
            {{ initial }}
          </AvatarFallback>
        </Avatar>
        <span
          v-if="totpOff"
          class="absolute -end-0.5 -top-0.5 size-2.5 rounded-full border-2 border-sidebar bg-warning"
          aria-hidden="true"
        />
      </span>
      <span v-if="!props.compact" class="flex min-w-0 flex-1 flex-col" aria-hidden="true">
        <span class="truncate text-detail font-semibold text-sidebar-foreground">{{ user.username }}</span>
        <span class="truncate text-xs text-sidebar-muted">{{ role }}</span>
      </span>
    </DropdownMenuTrigger>

    <DropdownMenuContent :side="props.compact ? 'right' : 'top'" align="start" class="w-60">
      <DropdownMenuLabel class="flex flex-col">
        <span class="truncate">{{ user.username }}</span>
        <span class="text-xs font-normal text-muted-foreground">{{ role }}</span>
      </DropdownMenuLabel>
      <DropdownMenuSeparator />
      <DropdownMenuItem v-if="totpOff" as-child class="text-warning">
        <RouterLink :to="{ name: 'settings-security' }" @click="emit('navigate')">
          <ShieldAlert class="text-warning" aria-hidden="true" />
          {{ t('shell.turnOnTotp') }}
        </RouterLink>
      </DropdownMenuItem>
      <DropdownMenuItem as-child>
        <RouterLink :to="{ name: 'settings-account' }" @click="emit('navigate')">
          <UserRound aria-hidden="true" />
          {{ t('common.nav.account') }}
        </RouterLink>
      </DropdownMenuItem>
      <DropdownMenuItem v-if="props.hasMessages" as-child>
        <RouterLink :to="{ name: 'messages' }" @click="emit('navigate')">
          <Megaphone aria-hidden="true" />
          <span class="flex-1">{{ t('common.nav.messages') }}</span>
          <span
            v-if="announcements.unread > 0"
            class="rounded-full bg-primary-soft px-1.5 text-caption text-primary-soft-foreground tabular-nums"
          >
            <span class="sr-only">{{ t('messages.bell', { count: announcements.unread }, announcements.unread) }}</span>
            <span aria-hidden="true">{{ announcements.unread }}</span>
          </span>
        </RouterLink>
      </DropdownMenuItem>
      <DropdownMenuSub>
        <DropdownMenuSubTrigger :disabled="switching">
          <Languages aria-hidden="true" />
          {{ t('common.language.label') }}
        </DropdownMenuSubTrigger>
        <DropdownMenuSubContent>
          <DropdownMenuRadioGroup :model-value="locale" @update:model-value="onLocale">
            <DropdownMenuRadioItem v-for="choice in choices" :key="choice.code" :value="choice.code">
              <span :lang="choice.code">{{ choice.name }}</span>
            </DropdownMenuRadioItem>
          </DropdownMenuRadioGroup>
        </DropdownMenuSubContent>
      </DropdownMenuSub>
      <DropdownMenuSeparator />
      <DropdownMenuItem :disabled="signingOut" @select="signOut">
        <LogOut aria-hidden="true" />
        {{ t('common.action.signOut') }}
      </DropdownMenuItem>
    </DropdownMenuContent>
  </DropdownMenu>
</template>
