<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { absolutePath, pathSegments } from '@/files/paths'
import { cn } from '@/lib/utils'

// The folder shown, as it is on the server (interface spec, section 9
// "Breadcrumb"): the root's own path first, then one button per folder.
const props = defineProps<{ root: string; path: string }>()
const emit = defineEmits<{ navigate: [path: string] }>()

const { t } = useI18n()
const segments = computed(() => pathSegments(props.path))
const base = computed(() => absolutePath(props.root, ''))

const crumb =
  'inline-flex h-7 max-w-full items-center rounded-sm px-1.5 outline-none hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring/50 max-md:h-11 max-md:min-w-11 max-md:justify-center'
</script>

<template>
  <nav :aria-label="t('files.breadcrumb')" class="min-w-0" dir="ltr">
    <ol class="m-0 flex min-w-0 list-none flex-wrap items-center p-0 font-mono text-[13px]">
      <li class="flex min-w-0 items-center">
        <button
          type="button"
          :class="cn(crumb, segments.length === 0 ? 'font-semibold text-foreground' : 'text-muted-foreground')"
          :aria-current="segments.length === 0 ? 'location' : undefined"
          :title="base"
          @click="emit('navigate', '')"
        >
          <span class="truncate">{{ base }}</span>
        </button>
      </li>
      <li v-for="(segment, index) in segments" :key="segment.path" class="flex min-w-0 items-center">
        <span class="px-0.5 text-muted-foreground" aria-hidden="true">/</span>
        <button
          type="button"
          :class="cn(crumb, index === segments.length - 1 ? 'font-semibold text-foreground' : 'text-muted-foreground')"
          :aria-current="index === segments.length - 1 ? 'location' : undefined"
          @click="emit('navigate', segment.path)"
        >
          <span class="truncate">{{ segment.name }}</span>
        </button>
      </li>
    </ol>
  </nav>
</template>
