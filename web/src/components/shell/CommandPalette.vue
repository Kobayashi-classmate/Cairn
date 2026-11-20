<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter, type RouteLocationRaw } from 'vue-router'
import { CornerDownLeft, UserRound } from '@lucide/vue'
import {
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from '@/components/ui/command'
import type { NavItem } from '@/layouts/navigation'

// "Search or jump" (Ctrl K / Cmd K): every menu entry, the messages page
// included, plus account settings.
const props = defineProps<{ items: readonly NavItem[] }>()
const open = defineModel<boolean>('open', { required: true })

const { t, te } = useI18n()
const router = useRouter()

interface Section {
  key: string
  heading: string
  entries: { key: string; label: string; icon: NavItem['icon']; to: RouteLocationRaw }[]
}

const sections = computed<Section[]>(() => {
  const label = (item: NavItem) => (te(item.labelKey) ? t(item.labelKey) : item.key)
  const result: Section[] = []
  for (const item of props.items) {
    const key = item.group || 'general'
    let section = result.find((s) => s.key === key)
    if (!section) {
      section = { key, heading: item.group ? t(`shell.group.${item.group}`) : t('shell.palette.pages'), entries: [] }
      result.push(section)
    }
    section.entries.push({ key: item.key, label: label(item), icon: item.icon, to: item.to })
  }
  result.push({
    key: 'account',
    heading: t('shell.palette.account'),
    entries: [{ key: 'account', label: t('common.nav.account'), icon: UserRound, to: { name: 'settings-account' } }],
  })
  return result
})

async function go(to: RouteLocationRaw) {
  open.value = false
  await router.push(to)
}
</script>

<template>
  <CommandDialog v-model:open="open" :title="t('shell.palette.title')" :description="t('shell.palette.description')">
    <CommandInput :placeholder="t('shell.palette.placeholder')" />
    <CommandList class="max-h-[min(420px,60vh)] p-1">
      <CommandEmpty class="text-muted-foreground">{{ t('shell.palette.empty') }}</CommandEmpty>
      <CommandGroup v-for="section in sections" :key="section.key" :heading="section.heading">
        <CommandItem
          v-for="entry in section.entries"
          :key="`${section.key}:${entry.key}`"
          :value="`${section.key}:${entry.key}`"
          class="group"
          @select="go(entry.to)"
        >
          <component :is="entry.icon" aria-hidden="true" />
          <span class="flex-1 truncate">{{ entry.label }}</span>
          <CornerDownLeft class="hidden size-3.5 group-data-[highlighted]:block" aria-hidden="true" />
        </CommandItem>
      </CommandGroup>
    </CommandList>
  </CommandDialog>
</template>
