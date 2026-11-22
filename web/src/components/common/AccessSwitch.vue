<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { useId } from 'vue'
import { useI18n } from 'vue-i18n'
import { LoaderCircle, ShieldAlert, ShieldCheck } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Switch } from '@/components/ui/switch'
import { cn } from '@/lib/utils'
import Notice from './Notice.vue'

// A switch that widens what the panel may do as root (full-disk access,
// root terminals), for a page header: a button showing whether it is on,
// opening a small panel with the switch and what turning it on means. The
// page asks for confirmation and does the change (`update`).
const props = defineProps<{
  label: string
  /** What turning it on allows, in a sentence or two. */
  description: string
  on: boolean
  /** The server's configuration keeps it off. */
  locked?: boolean
  lockedText?: string
  busy?: boolean
}>()

const emit = defineEmits<{ update: [on: boolean] }>()

const { t } = useI18n()
const id = `access-${useId()}`

function onUpdate(value: boolean) {
  if (value !== props.on) emit('update', value)
}
</script>

<template>
  <Popover>
    <PopoverTrigger as-child>
      <Button variant="outline" class="max-md:h-11">
        <ShieldAlert v-if="on" class="text-warning" aria-hidden="true" />
        <ShieldCheck v-else aria-hidden="true" />
        {{ label }}
        <span
          :class="
            cn(
              'rounded-full px-1.5 text-caption',
              on ? 'bg-warning-soft text-warning' : 'bg-muted text-muted-foreground',
            )
          "
        >
          {{ on ? t('common.status.on') : t('common.status.off') }}
        </span>
      </Button>
    </PopoverTrigger>
    <PopoverContent align="end" class="flex w-[min(360px,calc(100vw-32px))] flex-col gap-3">
      <div class="flex items-start gap-3">
        <Switch
          :id="id"
          :model-value="on"
          :disabled="locked || busy"
          :aria-describedby="`${id}-hint`"
          class="mt-0.5"
          @update:model-value="onUpdate"
        />
        <div class="flex min-w-0 flex-col gap-1">
          <label :for="id" class="flex cursor-pointer items-center gap-2 text-detail font-medium">
            {{ label }}
            <LoaderCircle v-if="busy" class="size-3.5 animate-spin text-muted-foreground" aria-hidden="true" />
          </label>
          <p :id="`${id}-hint`" class="m-0 text-xs text-muted-foreground">{{ description }}</p>
        </div>
      </div>
      <Notice v-if="locked && lockedText" tone="info" class="text-detail">{{ lockedText }}</Notice>
    </PopoverContent>
  </Popover>
</template>
