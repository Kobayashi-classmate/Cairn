<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { ref, type HTMLAttributes } from 'vue'
import { useI18n } from 'vue-i18n'
import { Eye, EyeOff } from '@lucide/vue'
import { cn } from '@/lib/utils'

// A password field with a button that shows the typed text.
defineOptions({ inheritAttrs: false })

const props = defineProps<{
  class?: HTMLAttributes['class']
  /** "lg" is the 40px field of the sign-in pages. */
  size?: 'default' | 'lg'
}>()
const model = defineModel<string>({ required: true })

const { t } = useI18n()
const visible = ref(false)
const input = ref<HTMLInputElement | null>(null)

defineExpose({ focus: () => input.value?.focus() })
</script>

<template>
  <div :class="cn('relative', props.class)">
    <input
      ref="input"
      v-model="model"
      v-bind="$attrs"
      :type="visible ? 'text' : 'password'"
      data-slot="input"
      :class="size === 'lg' ? 'h-10 max-md:h-11' : 'h-9'"
      class="w-full min-w-0 rounded-md border border-input bg-card py-1 ps-3 pe-10 text-base transition-[color,box-shadow] outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-destructive/20 md:text-sm"
    />
    <button
      type="button"
      class="absolute inset-y-0 end-0 flex w-10 items-center justify-center rounded-e-md text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
      :aria-label="visible ? t('common.action.hidePassword') : t('common.action.showPassword')"
      :aria-pressed="visible"
      @click="visible = !visible"
    >
      <EyeOff v-if="visible" class="size-4" aria-hidden="true" />
      <Eye v-else class="size-4" aria-hidden="true" />
    </button>
  </div>
</template>
