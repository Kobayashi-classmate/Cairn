<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { HTMLAttributes } from 'vue'
import { useI18n } from 'vue-i18n'
import { CircleAlert, RotateCw } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { errorMessage } from '@/i18n/errors'
import { cn } from '@/lib/utils'

// Loading failed (interface spec, section 7): the reason and "Retry", in
// place of the content that could not be loaded.
const props = defineProps<{
  error: unknown
  title?: string
  /** Less padding, for cards and rows. */
  compact?: boolean
  /** Hide the retry button (nothing to retry). */
  noRetry?: boolean
  class?: HTMLAttributes['class']
}>()

const emit = defineEmits<{ retry: [] }>()

const { t } = useI18n()
</script>

<template>
  <div
    role="alert"
    :class="cn('flex flex-col items-center gap-2 px-4 text-center', compact ? 'py-6' : 'py-12', props.class)"
  >
    <CircleAlert class="size-6 text-destructive" aria-hidden="true" />
    <p class="m-0 font-medium">{{ title ?? t('common.state.loadFailed') }}</p>
    <p class="m-0 max-w-[520px] text-detail text-muted-foreground">{{ errorMessage(error) }}</p>
    <Button v-if="!noRetry" variant="outline" size="sm" class="mt-2" @click="emit('retry')">
      <RotateCw aria-hidden="true" />
      {{ t('common.action.retry') }}
    </Button>
  </div>
</template>
