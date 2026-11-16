<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { NButton, NIcon, NModal, NText } from 'naive-ui'
import { CopyOutline } from '@vicons/ionicons5'
import { useCopy } from '@/composables/useCopy'

/** Shows a revealed password once, with a copy button; it is forgotten when closed. */
const password = defineModel<string | null>('password', { required: true })
defineProps<{ title: string }>()

const { t } = useI18n()
const copy = useCopy()
</script>

<template>
  <NModal
    :show="password !== null"
    preset="card"
    :title="title"
    class="password-dialog"
    @update:show="(open: boolean) => !open && (password = null)"
  >
    <NText depth="3" tag="p" class="hint">{{ t('databases.password.hint') }}</NText>
    <div class="secret">
      <code class="secret__value" dir="ltr">{{ password }}</code>
      <NButton @click="password && copy(password)">
        <template #icon>
          <NIcon><CopyOutline /></NIcon>
        </template>
        {{ t('common.action.copy') }}
      </NButton>
    </div>
  </NModal>
</template>

<style scoped>
.password-dialog {
  inline-size: min(480px, calc(100vw - 32px));
}

.hint {
  margin-block: 0 12px;
  font-size: 13px;
}

.secret {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}

.secret__value {
  flex: 1 1 220px;
  padding: 8px 10px;
  border-radius: 4px;
  background: rgb(128 128 128 / 12%);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  overflow-wrap: anywhere;
  user-select: all;
}
</style>
