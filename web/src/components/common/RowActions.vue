<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { Ellipsis } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import type { RowAction } from './types'

// The "more" menu of a table row (interface spec, sections 1 and 9): common
// actions first, destructive ones separated at the end in the danger colour.
const props = defineProps<{
  actions: readonly RowAction[]
  /** Accessible name of the button, naming the row ("More actions for site.test"). */
  label: string
}>()

const router = useRouter()

const regular = computed(() => props.actions.filter((action) => !action.destructive))
const destructive = computed(() => props.actions.filter((action) => action.destructive))

function run(action: RowAction) {
  if (action.href) {
    window.open(action.href, '_blank', 'noopener,noreferrer')
  } else if (action.to) {
    void router.push(action.to)
  } else {
    action.onSelect?.()
  }
}
</script>

<template>
  <DropdownMenu v-if="actions.length > 0">
    <DropdownMenuTrigger as-child>
      <Button variant="outline" size="icon" class="max-md:size-11" :aria-label="label" :title="label">
        <Ellipsis aria-hidden="true" />
      </Button>
    </DropdownMenuTrigger>
    <DropdownMenuContent align="end" class="min-w-44">
      <DropdownMenuItem
        v-for="action in regular"
        :key="action.key"
        :disabled="action.disabled"
        class="max-md:min-h-11"
        @select="run(action)"
      >
        <component :is="action.icon" v-if="action.icon" aria-hidden="true" />
        {{ action.label }}
      </DropdownMenuItem>
      <template v-if="destructive.length > 0">
        <DropdownMenuSeparator v-if="regular.length > 0" />
        <DropdownMenuItem
          v-for="action in destructive"
          :key="action.key"
          variant="destructive"
          :disabled="action.disabled"
          class="max-md:min-h-11"
          @select="run(action)"
        >
          <component :is="action.icon" v-if="action.icon" aria-hidden="true" />
          {{ action.label }}
        </DropdownMenuItem>
      </template>
    </DropdownMenuContent>
  </DropdownMenu>
</template>
