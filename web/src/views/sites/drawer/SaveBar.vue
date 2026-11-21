<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { LoaderCircle } from '@lucide/vue'
import Notice from '@/components/common/Notice.vue'
import { Button } from '@/components/ui/button'
import { errorMessage } from '@/i18n/errors'

// The buttons under a drawer sub-item's fields: apply the change (a job the
// job dock follows) or put the fields back.
defineProps<{
  dirty: boolean
  saving: boolean
  /** A change of the site is being applied. */
  applying: boolean
  error?: unknown
}>()

const emit = defineEmits<{ save: []; reset: [] }>()

const { t } = useI18n()
</script>

<template>
  <div class="flex flex-col gap-3">
    <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
    <Notice v-else-if="applying && !saving" tone="info">{{ t('sites.settings.applying') }}</Notice>
    <div class="flex flex-wrap items-center gap-2">
      <Button type="button" class="max-md:h-11" :disabled="!dirty || saving || applying" @click="emit('save')">
        <LoaderCircle v-if="saving" class="animate-spin" aria-hidden="true" />
        {{ t('sites.settings.apply') }}
      </Button>
      <Button v-if="dirty" type="button" variant="ghost" class="max-md:h-11" :disabled="saving" @click="emit('reset')">
        {{ t('sites.settings.discard') }}
      </Button>
    </div>
  </div>
</template>
