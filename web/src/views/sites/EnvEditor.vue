<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { Plus, Trash2 } from '@lucide/vue'
import type { EnvVar } from '@/api/sites'
import PasswordInput from '@/components/common/PasswordInput.vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { MAX_ENV } from '@/sites/validate'

// Environment variables of an app: one row per variable, values hidden
// until shown (they often hold secrets).
const model = defineModel<EnvVar[]>({ required: true })
defineProps<{ disabled?: boolean; describedBy?: string; invalid?: boolean }>()

const { t } = useI18n()

function add() {
  model.value = [...model.value, { name: '', value: '' }]
}

function remove(index: number) {
  model.value = model.value.filter((_, i) => i !== index)
}
</script>

<template>
  <div class="flex flex-col gap-2" role="group" :aria-describedby="describedBy">
    <div v-for="(variable, index) in model" :key="index" class="flex items-start gap-2">
      <Input
        v-model="variable.name"
        :aria-label="t('sites.app.envNameLabel', { index: index + 1 })"
        :aria-invalid="invalid || undefined"
        :placeholder="t('sites.app.envName')"
        autocomplete="off"
        spellcheck="false"
        :disabled="disabled"
        class="flex-[2_1_0] font-mono"
        dir="ltr"
      />
      <PasswordInput
        v-model="variable.value"
        :aria-label="t('sites.app.envValueLabel', { index: index + 1 })"
        :placeholder="t('sites.app.envValue')"
        autocomplete="off"
        spellcheck="false"
        :disabled="disabled"
        class="flex-[3_1_0] [&_input]:font-mono"
        dir="ltr"
      />
      <Button
        type="button"
        variant="ghost"
        size="icon-lg"
        class="shrink-0 text-muted-foreground max-md:size-11"
        :aria-label="t('sites.app.removeEnv', { name: variable.name || index + 1 })"
        :disabled="disabled"
        @click="remove(index)"
      >
        <Trash2 aria-hidden="true" />
      </Button>
    </div>
    <Button
      type="button"
      variant="outline"
      size="sm"
      class="self-start max-md:h-11"
      :disabled="disabled || model.length >= MAX_ENV"
      @click="add"
    >
      <Plus aria-hidden="true" />
      {{ t('sites.app.addEnv') }}
    </Button>
  </div>
</template>
