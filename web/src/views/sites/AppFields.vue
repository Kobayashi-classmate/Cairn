<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import FormField from '@/components/common/FormField.vue'
import SelectField from '@/components/common/SelectField.vue'
import SwitchField from '@/components/common/SwitchField.vue'
import type { SelectOption } from '@/components/common/types'
import { Input } from '@/components/ui/input'
import { useNavRoutes } from '@/composables/useNavRoutes'
import type { AppForm, FieldErrors } from '@/sites/form'
import type { InstalledRuntime } from '@/sites/useRuntimes'
import EnvEditor from './EnvEditor.vue'

// The fields of an app site: runtime, command, port, WebSocket and
// environment; with `advanced`, working directory, limits and the health
// check too.
const props = defineProps<{
  app: AppForm
  errors: FieldErrors
  runtimes: readonly InstalledRuntime[]
  mode: 'create' | 'edit'
  /** Which fields: the main ones, the advanced ones, or both. */
  part?: 'main' | 'advanced' | 'all'
  disabled?: boolean
  /** The values of the environment were not sent (read-only accounts): they show as hidden. */
  envHidden?: boolean
  idPrefix: string
}>()

const { t } = useI18n()
const navRoutes = useNavRoutes()

/** Example commands per runtime, shown in the empty command field (commands, not text to translate). */
const COMMAND_EXAMPLES: Record<string, string> = {
  nodejs: 'node server.js',
  python: 'gunicorn -b 127.0.0.1:$PORT app:app',
  go: './server',
  java: 'java -jar app.jar --server.port=$PORT',
  dotnet: 'dotnet MyApp.dll --urls http://127.0.0.1:$PORT',
}

const commandExample = computed(() => COMMAND_EXAMPLES[props.app.runtime.split('@')[0] ?? ''] ?? './server')

const runtimeOptions = computed<SelectOption[]>(() => {
  const options: SelectOption[] = [
    { value: '', label: t('sites.app.systemTools') },
    ...props.runtimes.map((r) => ({ value: `${r.software}@${r.line}`, label: `${r.name} ${r.line}` })),
  ]
  // A runtime the site uses but that is no longer installed stays visible.
  if (props.app.runtime && !options.some((o) => o.value === props.app.runtime)) {
    options.push({
      value: props.app.runtime,
      label: t('sites.app.notInstalled', { runtime: props.app.runtime.replace('@', ' ') }),
    })
  }
  return options
})

const showMain = computed(() => props.part !== 'advanced')
const showAdvanced = computed(() => props.part !== 'main')
</script>

<template>
  <template v-if="showMain">
    <FormField :id="`${idPrefix}-runtime`" :label="t('common.field.app.runtime')" :hint="t('sites.app.runtimeHint')">
      <template #default="field">
        <SelectField v-bind="field" v-model="app.runtime" :options="runtimeOptions" :disabled="disabled" />
      </template>
    </FormField>
    <p v-if="navRoutes.has('software')" class="-mt-3 m-0 text-xs">
      <RouterLink :to="{ name: 'software' }" class="text-link underline-offset-2 hover:underline tap-y">
        {{ t('sites.app.installRuntime') }}
      </RouterLink>
    </p>

    <FormField
      :id="`${idPrefix}-command`"
      :label="t('common.field.app.command')"
      :hint="t('sites.app.commandHint')"
      :error="errors['app.command']"
    >
      <template #default="field">
        <Input
          v-bind="field"
          v-model="app.command"
          :placeholder="commandExample"
          autocomplete="off"
          spellcheck="false"
          dir="ltr"
          class="font-mono"
          :disabled="disabled"
        />
      </template>
    </FormField>

    <FormField
      :id="`${idPrefix}-port`"
      :label="t('common.field.app.port')"
      :hint="mode === 'create' ? t('sites.app.portHintCreate') : t('sites.app.portHintEdit')"
      :error="errors['app.port']"
    >
      <template #default="field">
        <Input
          v-bind="field"
          v-model="app.port"
          inputmode="numeric"
          autocomplete="off"
          :placeholder="mode === 'create' ? t('sites.app.portAuto') : t('sites.app.portKeep')"
          class="w-56 font-mono"
          :disabled="disabled"
        />
      </template>
    </FormField>

    <SwitchField
      v-model="app.webSocket"
      :label="t('sites.form.webSocket')"
      :hint="t('sites.form.webSocketHint')"
      :disabled="disabled"
    />

    <FormField
      :id="`${idPrefix}-env`"
      :label="t('common.field.app.env')"
      :hint="envHidden ? t('sites.app.envHiddenHint') : t('sites.app.envHint')"
      :error="errors['app.env']"
    >
      <template #default="field">
        <EnvEditor
          v-model="app.env"
          :described-by="field['aria-describedby']"
          :invalid="!!field['aria-invalid']"
          :disabled="disabled || envHidden"
          :values-hidden="envHidden"
        />
      </template>
    </FormField>
  </template>

  <template v-if="showAdvanced">
    <FormField
      :id="`${idPrefix}-workdir`"
      :label="t('common.field.app.workDir')"
      :hint="t('sites.app.workDirHint')"
      :error="errors['app.workDir']"
    >
      <template #default="field">
        <Input
          v-bind="field"
          v-model="app.workDir"
          :placeholder="t('sites.overview.rootDefault')"
          autocomplete="off"
          spellcheck="false"
          dir="ltr"
          class="font-mono"
          :disabled="disabled"
        />
      </template>
    </FormField>

    <div class="grid gap-4 sm:grid-cols-2">
      <FormField :id="`${idPrefix}-memory`" :label="t('common.field.app.memoryMb')" :error="errors['app.memoryMb']">
        <template #default="field">
          <Input
            v-bind="field"
            v-model="app.memoryMb"
            inputmode="numeric"
            autocomplete="off"
            :placeholder="t('sites.app.noLimit')"
            class="font-mono"
            :disabled="disabled"
          />
        </template>
      </FormField>
      <FormField :id="`${idPrefix}-cpu`" :label="t('common.field.app.cpuPercent')" :error="errors['app.cpuPercent']">
        <template #default="field">
          <Input
            v-bind="field"
            v-model="app.cpuPercent"
            inputmode="numeric"
            autocomplete="off"
            :placeholder="t('sites.app.noLimit')"
            class="font-mono"
            :disabled="disabled"
          />
        </template>
      </FormField>
    </div>
    <p class="-mt-3 m-0 text-xs text-muted-foreground">{{ t('sites.app.limitsHint') }}</p>

    <FormField
      :id="`${idPrefix}-health`"
      :label="t('common.field.app.healthPath')"
      :hint="t('sites.app.healthHint')"
      :error="errors['app.healthPath']"
    >
      <template #default="field">
        <Input
          v-bind="field"
          v-model="app.healthPath"
          placeholder="/health"
          autocomplete="off"
          spellcheck="false"
          dir="ltr"
          class="font-mono"
          :disabled="disabled"
        />
      </template>
    </FormField>
  </template>
</template>
