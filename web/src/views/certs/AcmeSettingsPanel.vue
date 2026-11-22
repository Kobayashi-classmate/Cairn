<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ExternalLink, LoaderCircle } from '@lucide/vue'
import { isStepUpRequired } from '@/api/client'
import { fetchAcmeSettings, saveAcmeSettings, type AcmeCaRequest, type AcmeSettings } from '@/api/certs'
import Card from '@/components/common/Card.vue'
import ErrorState from '@/components/common/ErrorState.vue'
import FormField from '@/components/common/FormField.vue'
import Notice from '@/components/common/Notice.vue'
import PasswordInput from '@/components/common/PasswordInput.vue'
import SelectField from '@/components/common/SelectField.vue'
import StatusPill from '@/components/common/StatusPill.vue'
import type { SelectOption } from '@/components/common/types'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { Textarea } from '@/components/ui/textarea'
import { caAgreementUrl, caLabel } from '@/certs/labels'
import { isEmail } from '@/forms/rules'
import { errorMessage, validationMessage } from '@/i18n/errors'
import { notifySuccess } from '@/lib/notify'
import { useSessionStore } from '@/stores/session'

// ACME settings: the contact address, the default certificate authority and
// what each CA needs (External Account Binding, a custom directory).
// Saving asks for the password again (step-up).
const { t } = useI18n()
const session = useSessionStore()

interface CaEdit {
  eabKid: string
  eabHmac: string
  clearHmac: boolean
  directory: string
  rootPem: string
}

const settings = ref<AcmeSettings | null>(null)
const loadError = ref<unknown>(null)
const saving = ref(false)
const saveError = ref<unknown>(null)
const emailError = ref('')
/** By CA: an HMAC key typed without its key ID (the server would drop it). */
const kidErrors = ref<Record<string, string>>({})

/** Editable copy; EAB HMAC keys start empty (they are never returned). */
const form = reactive({ email: '', defaultCa: '', cas: {} as Record<string, CaEdit> })

const canManage = computed(() => session.can('certs.manage'))

async function load() {
  loadError.value = null
  try {
    const loaded = await fetchAcmeSettings()
    settings.value = loaded
    form.email = loaded.email
    form.defaultCa = loaded.defaultCa
    form.cas = Object.fromEntries(
      loaded.cas.map((ca) => [
        ca.name,
        { eabKid: ca.eabKid ?? '', eabHmac: '', clearHmac: false, directory: ca.directory ?? '', rootPem: ca.rootPem ?? '' },
      ]),
    )
  } catch (err) {
    loadError.value = err
  }
}

onMounted(load)

const caOptions = computed<SelectOption[]>(() =>
  (settings.value?.cas ?? []).map((ca) => ({ value: ca.name, label: caLabel(ca.name) })),
)

async function save() {
  const email = form.email.trim()
  emailError.value = email && !isEmail(email) ? validationMessage('email', 'email') : ''
  const missingKid: Record<string, string> = {}
  for (const [name, edit] of Object.entries(form.cas)) {
    if (edit.eabHmac.trim() && !edit.clearHmac && !edit.eabKid.trim()) missingKid[name] = t('certs.acme.kidNeeded')
  }
  kidErrors.value = missingKid
  if (emailError.value || Object.keys(missingKid).length > 0) return
  const cas: Record<string, AcmeCaRequest> = {}
  for (const ca of settings.value?.cas ?? []) {
    const edit = form.cas[ca.name]
    if (!edit) continue
    const change: AcmeCaRequest = {}
    const custom = ca.name === 'custom'
    if (ca.needsEab || custom) {
      if (ca.needsEab || edit.eabKid.trim()) change.eabKid = edit.eabKid.trim()
      // Empty keeps the stored key; "-" removes it.
      if (edit.clearHmac) change.eabHmac = '-'
      else if (edit.eabHmac) change.eabHmac = edit.eabHmac
    }
    if (custom) {
      change.directory = edit.directory.trim()
      change.rootPem = edit.rootPem
    }
    if (Object.keys(change).length > 0) cas[ca.name] = change
  }
  saving.value = true
  saveError.value = null
  try {
    await saveAcmeSettings({ email, defaultCa: form.defaultCa, cas })
    notifySuccess(t('certs.acme.saved'))
    await load()
  } catch (err) {
    if (!isStepUpRequired(err)) saveError.value = err
  } finally {
    saving.value = false
  }
}

function setClear(name: string, value: boolean | 'indeterminate') {
  const edit = form.cas[name]
  if (edit) edit.clearHmac = value === true
}
</script>

<template>
  <div class="flex flex-col gap-4">
    <Notice v-if="!canManage" tone="info">{{ t('settings.panel.readOnly') }}</Notice>

    <ErrorState v-if="loadError" :error="loadError" class="rounded-lg border bg-card" @retry="load" />
    <template v-else-if="!settings">
      <Skeleton class="h-40 w-full rounded-lg" />
      <Skeleton class="h-32 w-full rounded-lg" />
    </template>

    <form v-else class="flex flex-col gap-4" novalidate @submit.prevent="save">
      <Card :title="t('certs.acme.account')" :description="t('certs.acme.intro')" flush>
        <div class="divide-y">
          <div class="grid gap-3 px-4 py-4 md:grid-cols-[minmax(0,1fr)_minmax(0,320px)] md:items-start md:px-[18px]">
            <div class="flex flex-col gap-0.5">
              <label for="acme-email" class="font-medium">{{ t('common.field.email') }}</label>
              <p id="acme-email-hint" class="m-0 text-detail text-muted-foreground">{{ t('certs.acme.emailHint') }}</p>
            </div>
            <div class="flex flex-col gap-1">
              <Input
                id="acme-email"
                v-model="form.email"
                type="email"
                autocomplete="email"
                aria-describedby="acme-email-hint"
                :aria-invalid="emailError ? true : undefined"
                :disabled="!canManage || saving"
              />
              <p v-if="emailError" class="m-0 text-xs font-medium text-destructive">{{ emailError }}</p>
            </div>
          </div>
          <div class="grid gap-3 px-4 py-4 md:grid-cols-[minmax(0,1fr)_minmax(0,320px)] md:items-start md:px-[18px]">
            <div class="flex flex-col gap-0.5">
              <label for="acme-default-ca" class="font-medium">{{ t('common.field.defaultCa') }}</label>
              <p id="acme-default-ca-hint" class="m-0 text-detail text-muted-foreground">{{ t('certs.acme.defaultHint') }}</p>
            </div>
            <SelectField
              id="acme-default-ca"
              v-model="form.defaultCa"
              :options="caOptions"
              aria-describedby="acme-default-ca-hint"
              :disabled="!canManage || saving"
            />
          </div>
        </div>
      </Card>

      <Card v-for="ca in settings.cas" :key="ca.name" flush>
        <template #title>
          <span class="inline-flex flex-wrap items-center gap-2">
            {{ caLabel(ca.name) }}
            <StatusPill :tone="ca.ready ? 'success' : 'warning'">
              {{ ca.ready ? t('certs.acme.ready') : t('certs.acme.notReady') }}
            </StatusPill>
          </span>
        </template>
        <template v-if="caAgreementUrl(ca.name)" #actions>
          <a
            :href="caAgreementUrl(ca.name)!"
            target="_blank"
            rel="noopener noreferrer"
            class="inline-flex items-center gap-1 text-detail text-link underline-offset-2 hover:underline tap-y"
          >
            {{ t('certs.issue.agreementLink') }}
            <ExternalLink class="size-3" aria-hidden="true" />
          </a>
        </template>
        <div class="flex flex-col gap-4 px-4 py-4 md:px-[18px]">
          <p v-if="ca.directory && ca.name !== 'custom'" class="m-0 font-mono text-xs break-all text-muted-foreground" dir="ltr">
            {{ ca.directory }}
          </p>
          <template v-if="ca.name === 'custom' && form.cas[ca.name]">
            <p class="m-0 text-detail text-muted-foreground">{{ t('certs.acme.customIntro') }}</p>
            <FormField :id="`acme-${ca.name}-directory`" :label="t('common.field.ca.directory')">
              <template #default="field">
                <Input
                  v-bind="field"
                  v-model="form.cas[ca.name]!.directory"
                  type="url"
                  spellcheck="false"
                  dir="ltr"
                  class="font-mono"
                  :disabled="!canManage || saving"
                />
              </template>
            </FormField>
            <FormField :id="`acme-${ca.name}-root`" :label="t('common.field.ca.rootPem')" :hint="t('certs.acme.rootHint')">
              <template #default="field">
                <Textarea
                  v-bind="field"
                  v-model="form.cas[ca.name]!.rootPem"
                  rows="4"
                  spellcheck="false"
                  dir="ltr"
                  class="max-h-48 font-mono text-xs"
                  :disabled="!canManage || saving"
                />
              </template>
            </FormField>
          </template>
          <template v-if="(ca.needsEab || ca.name === 'custom') && form.cas[ca.name]">
            <p v-if="ca.needsEab" class="m-0 text-detail text-muted-foreground">{{ t('certs.acme.eabIntro') }}</p>
            <div class="grid gap-4 md:grid-cols-2">
              <FormField :id="`acme-${ca.name}-kid`" :label="t('certs.acme.eabKid')" :error="kidErrors[ca.name]">
                <template #default="field">
                  <Input
                    v-bind="field"
                    v-model="form.cas[ca.name]!.eabKid"
                    autocomplete="off"
                    spellcheck="false"
                    dir="ltr"
                    class="font-mono"
                    :disabled="!canManage || saving"
                  />
                </template>
              </FormField>
              <FormField
                :id="`acme-${ca.name}-hmac`"
                :label="t('certs.acme.eabHmac')"
                :hint="ca.eabHmacSet ? t('dns.dialog.secretStored') : undefined"
              >
                <template #default="field">
                  <PasswordInput
                    v-bind="field"
                    v-model="form.cas[ca.name]!.eabHmac"
                    autocomplete="new-password"
                    dir="ltr"
                    class="[&_input]:font-mono"
                    :disabled="!canManage || saving || form.cas[ca.name]!.clearHmac"
                  />
                </template>
              </FormField>
            </div>
            <label v-if="ca.eabHmacSet" class="flex cursor-pointer items-center gap-2 text-detail">
              <Checkbox
                :model-value="form.cas[ca.name]!.clearHmac"
                :disabled="!canManage || saving"
                @update:model-value="(value: boolean | 'indeterminate') => setClear(ca.name, value)"
              />
              {{ t('certs.acme.clearHmac') }}
            </label>
          </template>
          <p v-if="!ca.needsEab && ca.name !== 'custom'" class="m-0 text-detail text-muted-foreground">
            {{ t('certs.acme.nothingToSet') }}
          </p>
        </div>
      </Card>

      <Notice v-if="saveError" tone="destructive">{{ errorMessage(saveError) }}</Notice>
      <div v-if="canManage" class="flex flex-wrap items-center gap-3">
        <Button type="submit" class="max-md:h-11" :disabled="saving">
          <LoaderCircle v-if="saving" class="animate-spin" aria-hidden="true" />
          {{ t('certs.acme.save') }}
        </Button>
        <span class="text-detail text-muted-foreground">{{ t('certs.acme.stepUp') }}</span>
      </div>
    </form>
  </div>
</template>
