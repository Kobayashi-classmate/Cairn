<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router'
import { useMediaQuery } from '@vueuse/core'
import { Menu } from '@lucide/vue'
import { toast } from 'vue-sonner'
import type { Announcement } from '@/api/announcements'
import CairnLogo from '@/components/common/CairnLogo.vue'
import ConfirmHost from '@/components/common/ConfirmHost.vue'
import Notice from '@/components/common/Notice.vue'
import ThemeSwitcher from '@/components/common/ThemeSwitcher.vue'
import JobDock from '@/components/jobs/JobDock.vue'
import AppSidebar from '@/components/shell/AppSidebar.vue'
import CommandPalette from '@/components/shell/CommandPalette.vue'
import ElevationIndicator from '@/components/shell/ElevationIndicator.vue'
import { Button } from '@/components/ui/button'
import { Sheet, SheetContent, SheetDescription, SheetTitle } from '@/components/ui/sheet'
import { useAnnouncementPolling } from '@/composables/useAnnouncementPolling'
import { pickLocalized } from '@/i18n'
import { errorMessage } from '@/i18n/errors'
import { useAnnouncementsStore } from '@/stores/announcements'
import { useNavStore } from '@/stores/nav'
import { homeLocation, resolveNav } from './navigation'

// The signed-in shell (interface spec, section 6). At 1024px and wider the
// sidebar is fixed; from 768px it folds into a 64px icon rail; below that a
// top bar opens the menu in a sheet from the left.
const { t, te } = useI18n()
const route = useRoute()
const router = useRouter()
const nav = useNavStore()
const announcements = useAnnouncementsStore()

useAnnouncementPolling()

const items = computed(() => resolveNav(nav.entries, router))
const home = computed(() => homeLocation(router))

const isDesktop = useMediaQuery('(min-width: 1024px)')
const isTablet = useMediaQuery('(min-width: 768px)')
const rail = computed(() => isTablet.value && !isDesktop.value)

const mobileMenuOpen = ref(false)
const paletteOpen = ref(false)

// Leaving the page (or growing past the phone layout) closes the menu sheet.
watch(() => route.fullPath, () => {
  mobileMenuOpen.value = false
})
watch(isTablet, (wide) => {
  if (wide) mobileMenuOpen.value = false
})

const pageTitle = computed(() => {
  const key = route.meta.titleKey
  return key && te(key) ? t(key) : t('common.app.name')
})

// Screen readers do not notice a client-side page change; say the new page's
// name once it has rendered (tabs and drawers only change the query).
const routeAnnouncement = ref('')
let announceTimer = 0
watch(
  () => route.path,
  () => {
    routeAnnouncement.value = ''
    window.clearTimeout(announceTimer)
    announceTimer = window.setTimeout(() => {
      routeAnnouncement.value = pageTitle.value
    }, 150)
  },
)
onBeforeUnmount(() => window.clearTimeout(announceTimer))

const isMac = /Mac|iPhone|iPad/i.test(navigator.platform || navigator.userAgent)
const shortcut = computed(() => (isMac ? t('shell.shortcut.mac') : t('shell.shortcut.other')))

function onKeydown(event: KeyboardEvent) {
  if (!(event.metaKey || event.ctrlKey) || event.altKey || event.key.toLowerCase() !== 'k') return
  // The terminal and the code editor keep their own Ctrl K.
  const target = event.target instanceof Element ? event.target : null
  if (target?.closest('.xterm, .cm-editor')) return
  event.preventDefault()
  mobileMenuOpen.value = false
  paletteOpen.value = !paletteOpen.value
}

onMounted(() => window.addEventListener('keydown', onKeydown))
onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))

function openPalette() {
  mobileMenuOpen.value = false
  paletteOpen.value = true
}

async function markBannerRead(item: Announcement) {
  try {
    await announcements.markRead(item.id)
  } catch (err) {
    toast.error(errorMessage(err))
  }
}
</script>

<template>
  <div class="min-h-dvh bg-background">
    <a
      href="#main"
      class="sr-only z-50 rounded-md bg-card px-3 py-2 text-sm font-medium shadow-overlay focus:not-sr-only focus:fixed focus:start-3 focus:top-3"
    >
      {{ t('shell.skipToContent') }}
    </a>

    <!-- Sidebar: fixed from 1024px, icon rail from 768px. It holds the logo,
         search, menu and account, so it is the page's banner landmark; on
         phones the top bar below takes that role. -->
    <header
      class="fixed inset-y-0 start-0 z-30 hidden w-16 border-e border-sidebar-border md:block lg:w-[232px]"
    >
      <AppSidebar :items="items" :home="home" :rail="rail" :shortcut="shortcut" @search="openPalette" />
    </header>

    <!-- Phone: top bar and the menu in a sheet -->
    <header
      class="sticky top-0 z-30 flex h-14 items-center gap-2 border-b border-border bg-background/95 px-3 backdrop-blur md:hidden"
    >
      <Button variant="ghost" size="icon-lg" class="size-11" :aria-label="t('shell.openMenu')" :aria-expanded="mobileMenuOpen" @click="mobileMenuOpen = true">
        <Menu aria-hidden="true" />
      </Button>
      <RouterLink v-slot="{ href, navigate }" :to="home" custom>
        <a
          :href="href"
          class="-mx-1.5 inline-flex shrink-0 items-center justify-center rounded-md p-2.5 outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
          :aria-label="t('common.app.name')"
          @click="navigate"
        >
          <CairnLogo class="size-6" />
        </a>
      </RouterLink>
      <span class="min-w-0 flex-1 truncate font-semibold">{{ pageTitle }}</span>
      <ElevationIndicator compact />
      <ThemeSwitcher icon-only />
    </header>
    <Sheet v-model:open="mobileMenuOpen">
      <SheetContent side="left" class="w-[280px] max-w-[85vw] gap-0 border-sidebar-border bg-sidebar p-0 sm:max-w-[280px]">
        <SheetTitle class="sr-only">{{ t('shell.menu') }}</SheetTitle>
        <SheetDescription class="sr-only">{{ t('shell.mainNav') }}</SheetDescription>
        <AppSidebar :items="items" :home="home" :shortcut="shortcut" @search="openPalette" />
      </SheetContent>
    </Sheet>

    <div class="md:ps-16 lg:ps-[232px]">
      <main
        id="main"
        tabindex="-1"
        class="mx-auto w-full max-w-[1240px] px-4 pt-4 pb-10 outline-none md:px-6 md:pt-6 md:pb-12 lg:px-8 lg:pt-8 lg:pb-14"
      >
        <!-- Unread critical official messages stay visible until read or dismissed. -->
        <div v-if="announcements.banners.length > 0" class="mb-5 flex flex-col gap-2">
          <Notice
            v-for="item in announcements.banners"
            :key="item.id"
            tone="destructive"
            dismissible
            @dismiss="announcements.dismiss(item.id)"
          >
            {{ pickLocalized(item.title) }}
            <template #action>
              <RouterLink
                :to="{ name: 'messages', query: { id: item.id } }"
                class="whitespace-nowrap text-current underline underline-offset-2 hover:no-underline"
              >
                {{ t('messages.view') }}
              </RouterLink>
              <button
                type="button"
                class="rounded-sm whitespace-nowrap underline-offset-2 outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring/50"
                @click="markBannerRead(item)"
              >
                {{ t('messages.markRead') }}
              </button>
            </template>
          </Notice>
        </div>
        <RouterView />
      </main>
    </div>

    <p class="sr-only" aria-live="polite" aria-atomic="true">{{ routeAnnouncement }}</p>
    <CommandPalette v-model:open="paletteOpen" :items="items" />
    <!-- Long jobs started from any page, followed in the bottom right corner. -->
    <JobDock />
    <ConfirmHost />
  </div>
</template>
