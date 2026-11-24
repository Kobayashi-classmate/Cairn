<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ArrowRightLeft, ListChecks, LoaderCircle } from '@lucide/vue'
import { CLOSE_AFTER_DEFAULT, CLOSE_AFTER_MAX, CLOSE_AFTER_MIN, changeSshPort, type SshView } from '@/api/ssh'
import Card from '@/components/common/Card.vue'
import FormField from '@/components/common/FormField.vue'
import Notice from '@/components/common/Notice.vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { confirmAction } from '@/composables/useConfirmDialog'
import { validationMessage } from '@/i18n/errors'
import { launchJob } from '@/jobs/dock'

// Moves SSH to another port in two steps: first both ports listen (the
// banner on top follows that), then the old one closes on its own after the
// chosen time, or earlier from the banner.
const props = defineProps<{ view: SshView; canManage: boolean }>()
const emit = defineEmits<{ changed: [] }>()

const { t } = useI18n()
const form = reactive({ port: '', closeAfter: String(CLOSE_AFTER_DEFAULT) })
const errors = ref<Partial<Record<'port' | 'closeAfter', string>>>({})
/** The move job runs. */
const moving = ref(false)

function number(value: string): number | null {
  return /^\d{1,5}$/.test(value.trim()) ? Number(value.trim()) : null
}

const port = computed(() => number(form.port))
const minutes = computed(() => number(form.closeAfter))
const editable = computed(() => props.canManage && props.view.installed && !props.view.pending && !moving.value)

const steps = computed(() => {
  const target = port.value ?? '…'
  const list = [t('ssh.port.steps.listen', { port: target })]
  if (props.view.firewall) list.push(t('ssh.port.steps.firewall', { port: target, firewall: props.view.firewall }))
  list.push(t('ssh.port.steps.selinux'), t('ssh.port.steps.check', { port: target }))
  list.push(t('ssh.port.steps.close', { old: props.view.ports.join(', '), port: target, minutes: minutes.value ?? CLOSE_AFTER_DEFAULT }))
  return list
})

function validate(): boolean {
  const next: typeof errors.value = {}
  const target = port.value
  if (target === null || target < 1 || target > 65535) next.port = validationMessage('port', 'port')
  else if (props.view.ports.includes(target)) next.port = t('ssh.port.samePort', { port: target })
  const wait = minutes.value
  if (wait === null || wait < CLOSE_AFTER_MIN || wait > CLOSE_AFTER_MAX) {
    next.closeAfter = t('ssh.port.closeAfterInvalid', { min: CLOSE_AFTER_MIN, max: CLOSE_AFTER_MAX })
  }
  errors.value = next
  return Object.keys(next).length === 0
}

async function start(target: number, wait: number) {
  moving.value = true
  try {
    // Step-up protected: the client asks for the password and retries.
    const job = await launchJob(() => changeSshPort(target, wait), {
      rethrow: true,
      onFinished: () => {
        moving.value = false
        emit('changed')
      },
    })
    if (!job) moving.value = false
  } catch (err) {
    moving.value = false
    throw err
  }
}

function move() {
  if (!validate()) return
  const target = port.value
  const wait = minutes.value
  if (target === null || wait === null) return
  void confirmAction({
    title: t('ssh.port.title', { port: target }),
    description: t('ssh.port.text', { minutes: wait }),
    confirmLabel: t('ssh.port.submit'),
    run: () => start(target, wait),
  })
}
</script>

<template>
  <form class="max-w-[720px]" novalidate @submit.prevent="move">
    <Card :title="t('ssh.port.cardTitle')" :description="t('ssh.port.intro')">
      <div class="flex flex-col gap-5">
        <p class="m-0 text-detail">
          {{ t('ssh.port.current', { ports: view.ports.join(', ') }, view.ports.length) }}
        </p>
        <Notice v-if="view.pending" tone="info">{{ t('ssh.port.inProgress') }}</Notice>
        <div class="flex flex-wrap gap-x-6 gap-y-4">
          <FormField id="ssh-port" :label="t('ssh.port.new')" :error="errors.port">
            <template #default="field">
              <Input v-bind="field" v-model="form.port" inputmode="numeric" autocomplete="off" class="w-40 font-mono" placeholder="2222" :disabled="!editable" />
            </template>
          </FormField>
          <FormField
            id="ssh-close-after"
            :label="t('ssh.port.closeAfter')"
            :hint="t('ssh.port.closeAfterHint', { min: CLOSE_AFTER_MIN, max: CLOSE_AFTER_MAX })"
            :error="errors.closeAfter"
          >
            <template #default="field">
              <div class="flex items-center gap-2">
                <Input v-bind="field" v-model="form.closeAfter" inputmode="numeric" autocomplete="off" class="w-24 font-mono" :disabled="!editable" />
                <span class="text-detail text-muted-foreground">{{ t('ssh.port.minutes') }}</span>
              </div>
            </template>
          </FormField>
        </div>

        <section class="flex flex-col gap-2" :aria-label="t('common.sheet.willDo')">
          <h3 class="m-0 flex items-center gap-1.5 text-detail font-semibold">
            <ListChecks class="size-4 text-muted-foreground" aria-hidden="true" />
            {{ t('common.sheet.willDo') }}
          </h3>
          <ol class="m-0 flex list-decimal flex-col gap-1.5 rounded-md bg-muted py-3 ps-8 pe-4 text-detail text-muted-foreground">
            <li v-for="(step, index) in steps" :key="index" class="[overflow-wrap:anywhere]">{{ step }}</li>
          </ol>
        </section>
      </div>
      <template v-if="canManage" #footer>
        <Button type="submit" class="ms-auto max-md:h-11" :disabled="!editable">
          <LoaderCircle v-if="moving" class="animate-spin" aria-hidden="true" />
          <ArrowRightLeft v-else aria-hidden="true" />
          {{ t('ssh.port.submit') }}
        </Button>
      </template>
    </Card>
  </form>
</template>
