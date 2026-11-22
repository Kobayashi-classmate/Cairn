<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { LoaderCircle } from '@lucide/vue'
import { PERMIT_ROOT_LOGIN, updateSshSettings, type PermitRootLogin, type SshView } from '@/api/ssh'
import Card from '@/components/common/Card.vue'
import KeyValueList from '@/components/common/KeyValueList.vue'
import KeyValueRow from '@/components/common/KeyValueRow.vue'
import Notice from '@/components/common/Notice.vue'
import SelectField from '@/components/common/SelectField.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { confirmAction } from '@/composables/useConfirmDialog'
import { launchJob } from '@/jobs/dock'

// How people sign in over SSH (passwords, root), saved as a job; and how
// the server stands.
const props = defineProps<{ view: SshView; canManage: boolean }>()
const emit = defineEmits<{ changed: [] }>()

const { t } = useI18n()
const form = reactive({ passwordAuthentication: true, permitRootLogin: 'prohibit-password' as string })
/** The settings job runs; the form waits for it. */
const saving = ref(false)

// The form starts from what the server has.
watch(
  () => [props.view.passwordAuthentication, props.view.permitRootLogin] as const,
  ([passwords, root]) => {
    form.passwordAuthentication = passwords
    form.permitRootLogin = root
  },
  { immediate: true },
)

const rootOptions = computed(() => PERMIT_ROOT_LOGIN.map((value) => ({ value, label: t(`ssh.permitRootLogin.${value}`) })))
const keyCount = computed(() => props.view.users.reduce((sum, user) => sum + user.keys, 0))
const changed = computed(
  () => form.passwordAuthentication !== props.view.passwordAuthentication || form.permitRootLogin !== props.view.permitRootLogin,
)
const editable = computed(() => props.canManage && props.view.installed && !saving.value)

function reset() {
  form.passwordAuthentication = props.view.passwordAuthentication
  form.permitRootLogin = props.view.permitRootLogin
}

async function start(rethrow: boolean) {
  const body = { passwordAuthentication: form.passwordAuthentication, permitRootLogin: form.permitRootLogin as PermitRootLogin }
  saving.value = true
  try {
    // Step-up protected: the client asks for the password and retries.
    const job = await launchJob(() => updateSshSettings(body), {
      rethrow,
      onFinished: () => {
        saving.value = false
        emit('changed')
      },
    })
    if (!job) saving.value = false
  } catch (err) {
    saving.value = false
    throw err
  }
}

function save() {
  if (!changed.value) return
  if (props.view.passwordAuthentication && !form.passwordAuthentication) {
    void confirmAction({
      title: t('ssh.settings.passwordsOffTitle'),
      description: t('ssh.settings.passwordsOffText'),
      confirmLabel: t('ssh.settings.passwordsOffSubmit'),
      destructive: true,
      run: () => start(true),
    })
    return
  }
  void start(false)
}
</script>

<template>
  <div class="grid items-start gap-4 lg:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
    <form novalidate @submit.prevent="save">
      <Card :title="t('ssh.settings.title')" :description="t('ssh.settings.intro')" flush>
        <div class="divide-y">
          <div class="grid gap-3 px-4 py-4 md:grid-cols-[minmax(0,1fr)_auto] md:items-start md:px-[18px]">
            <div class="flex flex-col gap-0.5">
              <label for="ssh-passwords" class="font-medium">{{ t('ssh.settings.passwords') }}</label>
              <p id="ssh-passwords-hint" class="m-0 text-detail text-muted-foreground">{{ t('ssh.settings.passwordsHint') }}</p>
            </div>
            <Switch
              id="ssh-passwords"
              v-model="form.passwordAuthentication"
              :disabled="!editable"
              aria-describedby="ssh-passwords-hint"
            />
          </div>
          <div v-if="!form.passwordAuthentication && keyCount === 0" class="px-4 py-3 md:px-[18px]">
            <Notice tone="destructive">{{ t('errors.ssh.no_keys') }}</Notice>
          </div>
          <div class="grid gap-3 px-4 py-4 md:grid-cols-[minmax(0,1fr)_minmax(0,260px)] md:items-start md:px-[18px]">
            <div class="flex flex-col gap-0.5">
              <label for="ssh-root" class="font-medium">{{ t('ssh.settings.root') }}</label>
              <p id="ssh-root-hint" class="m-0 text-detail text-muted-foreground">{{ t('ssh.settings.rootHint') }}</p>
            </div>
            <SelectField
              id="ssh-root"
              v-model="form.permitRootLogin"
              :options="rootOptions"
              :disabled="!editable"
              aria-describedby="ssh-root-hint"
            />
          </div>
        </div>
        <template v-if="canManage" #footer>
          <div class="ms-auto flex flex-wrap items-center gap-2">
            <Button v-if="changed && !saving" type="button" variant="ghost" class="max-md:h-11" @click="reset">
              {{ t('ssh.settings.discard') }}
            </Button>
            <Button type="submit" class="max-md:h-11" :disabled="!editable || !changed">
              <LoaderCircle v-if="saving" class="animate-spin" aria-hidden="true" />
              {{ t('ssh.settings.save') }}
            </Button>
          </div>
        </template>
      </Card>
    </form>

    <Card :title="t('ssh.status.title')" flush>
      <KeyValueList>
        <KeyValueRow :label="t('ssh.status.service')" mono>
          {{ view.service }}
          <span v-if="view.socketActivated" class="font-sans text-muted-foreground">{{ t('ssh.status.socket') }}</span>
        </KeyValueRow>
        <KeyValueRow :label="t('ssh.status.ports')" mono>{{ view.ports.join(', ') }}</KeyValueRow>
        <KeyValueRow :label="t('ssh.status.listen')" mono>
          <span dir="ltr">{{ view.listenAddresses.join(', ') }}</span>
        </KeyValueRow>
        <KeyValueRow :label="t('ssh.status.pubkey')">
          <StatusDot :tone="view.pubkeyAuthentication ? 'success' : 'neutral'">
            {{ view.pubkeyAuthentication ? t('ssh.status.on') : t('ssh.status.off') }}
          </StatusDot>
        </KeyValueRow>
        <KeyValueRow :label="t('ssh.status.firewall')">
          <span :class="view.firewall ? 'font-mono text-[12.5px]' : 'text-muted-foreground'">{{ view.firewall || t('ssh.status.noFirewall') }}</span>
        </KeyValueRow>
      </KeyValueList>
    </Card>
  </div>
</template>
