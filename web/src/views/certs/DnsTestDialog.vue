<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { LoaderCircle } from '@lucide/vue'
import { isApiError } from '@/api/client'
import { testDnsAccount, type DnsAccount } from '@/api/dns'
import FormField from '@/components/common/FormField.vue'
import Notice from '@/components/common/Notice.vue'
import { keepOpenOnCompanion, keepOpenOnEscape, trackOverlay } from '@/components/common/overlayGuard'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { errorMessage } from '@/i18n/errors'
import { normalizeHost } from '@/sites/validate'

// Checks a DNS account by listing the records of one zone; nothing changes.
const props = defineProps<{ account: DnsAccount | null }>()
const emit = defineEmits<{ close: [] }>()

const { t } = useI18n()
trackOverlay(() => props.account !== null)
const zone = ref('')
const busy = ref(false)
const error = ref<unknown>(null)
/** The last successful answer, read out by screen readers. */
const result = ref('')

watch(
  () => props.account,
  (account) => {
    if (!account) return
    zone.value = ''
    error.value = null
    result.value = ''
  },
)

const reason = computed(() =>
  isApiError(error.value) && typeof error.value.params.reason === 'string' ? error.value.params.reason : '',
)

async function run() {
  const account = props.account
  const name = normalizeHost(zone.value)
  if (!account || !name) return
  busy.value = true
  error.value = null
  result.value = ''
  try {
    const records = await testDnsAccount(account.id, name)
    result.value = t('dns.test.ok', { count: records, zone: name }, records)
  } catch (err) {
    error.value = err
  } finally {
    busy.value = false
  }
}

function onOpenChange(open: boolean) {
  if (!open) emit('close')
}
</script>

<template>
  <Dialog :open="account !== null" @update:open="onOpenChange">
    <DialogContent class="sm:max-w-[460px]" @interact-outside="keepOpenOnCompanion" @escape-key-down="keepOpenOnEscape">
      <form class="flex flex-col gap-4" novalidate @submit.prevent="run">
        <div class="flex flex-col gap-1.5">
          <DialogTitle>{{ account ? t('dns.test.title', { name: account.name }) : '' }}</DialogTitle>
          <DialogDescription class="m-0 text-muted-foreground">{{ t('dns.test.intro') }}</DialogDescription>
        </div>
        <FormField id="dns-test-zone" :label="t('common.field.zone')">
          <template #default="field">
            <Input
              v-bind="field"
              v-model="zone"
              :placeholder="t('dns.test.zonePlaceholder')"
              autocomplete="off"
              autocapitalize="none"
              spellcheck="false"
              dir="ltr"
              class="font-mono"
            />
          </template>
        </FormField>
        <Notice v-if="result" tone="success">{{ result }}</Notice>
        <Notice v-if="error" tone="destructive">{{ errorMessage(error) }}</Notice>
        <pre
          v-if="reason"
          tabindex="0"
          class="m-0 max-h-40 overflow-auto rounded-md bg-muted px-3 py-2 font-mono text-xs whitespace-pre-wrap [overflow-wrap:anywhere]"
          dir="ltr"
        >{{ reason }}</pre>
        <div class="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
          <Button type="button" variant="outline" class="max-md:h-11" @click="emit('close')">{{ t('common.action.close') }}</Button>
          <Button type="submit" class="max-md:h-11" :disabled="busy || !zone.trim()">
            <LoaderCircle v-if="busy" class="animate-spin" aria-hidden="true" />
            {{ t('dns.test.button') }}
          </Button>
        </div>
      </form>
    </DialogContent>
  </Dialog>
</template>
