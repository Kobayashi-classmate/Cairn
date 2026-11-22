<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// One block of a settings card (interface spec, section 6): the title and a
// sentence on the left, the control on the right; stacked when the card is
// narrow. Put rows in a flush Card, inside SettingRows.
defineProps<{
  title: string
  description?: string
  /** Id of the control the title labels (inputs and selects). */
  labelFor?: string
  /** Id of the title, for controls named with aria-labelledby. */
  titleId?: string
  /** Heading level of the title: 3, or 4 in a card that has a title of its own. */
  level?: 3 | 4
}>()
</script>

<template>
  <div
    class="flex flex-col gap-3 px-4 py-4 md:px-[18px] @lg:flex-row @lg:items-center @lg:justify-between @lg:gap-6"
  >
    <div class="flex min-w-0 flex-1 flex-col gap-0.5">
      <div class="flex flex-wrap items-center gap-2">
        <label v-if="labelFor" :id="titleId" :for="labelFor" class="text-sm font-semibold">{{ title }}</label>
        <component :is="level === 4 ? 'h4' : 'h3'" v-else :id="titleId" class="m-0 text-sm font-semibold">{{ title }}</component>
        <slot name="badge" />
      </div>
      <div v-if="description || $slots.description" class="text-detail text-muted-foreground [overflow-wrap:anywhere]">
        <slot name="description">{{ description }}</slot>
      </div>
    </div>
    <div v-if="$slots.default" class="flex min-w-0 shrink-0 flex-wrap items-center gap-2 @lg:justify-end">
      <slot />
    </div>
  </div>
</template>
