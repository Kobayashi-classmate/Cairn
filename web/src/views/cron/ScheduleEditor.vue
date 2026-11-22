<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Picks a schedule from presets or as a raw OnCalendar expression, and
// shows how systemd reads it with the next runs.
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { CalendarClock, LoaderCircle } from '@lucide/vue'
import { isAbortError, isApiError } from '@/api/client'
import { checkSchedule, type ScheduleInfo } from '@/api/cron'
import SelectField from '@/components/common/SelectField.vue'
import type { SelectOption } from '@/components/common/types'
import { Input } from '@/components/ui/input'
import { PRESET_KINDS, WEEKDAYS, fromExpression, isScheduleText, toExpression, type Preset } from '@/cron/schedule'
import { errorMessage } from '@/i18n/errors'
import { formatDateTime } from '@/i18n/format'

const expression = defineModel<string>('expression', { required: true })
const props = defineProps<{ id?: string; invalid?: boolean }>()

/** Wait after typing before asking the server. */
const CHECK_DELAY_MS = 400

const { t } = useI18n()
const preset = ref<Preset>(fromExpression(expression.value))
const info = ref<ScheduleInfo | null>(null)
const checkError = ref<unknown>(null)
const checking = ref(false)
let timer: ReturnType<typeof setTimeout> | undefined
let controller: AbortController | null = null

// A new expression from outside (another task opened) resets the presets.
watch(expression, (value) => {
  if (value !== toExpression(preset.value)) preset.value = fromExpression(value)
})

watch(
  preset,
  (value) => {
    const next = toExpression(value)
    if (next !== expression.value) expression.value = next
  },
  { deep: true },
)

const kindOptions = computed<SelectOption[]>(() => PRESET_KINDS.map((kind) => ({ value: kind, label: t(`cron.schedule.kind.${kind}`) })))
const weekdayOptions = computed<SelectOption[]>(() => WEEKDAYS.map((day) => ({ value: day, label: t(`cron.weekday.${day}`) })))

function two(n: number): string {
  return String(n).padStart(2, '0')
}

/** The time of day as the time field shows it ("03:00"). */
const time = computed({
  get: () => `${two(Number(preset.value.hour) || 0)}:${two(Number(preset.value.minute) || 0)}`,
  set: (value: string | number) => {
    const match = /^(\d{1,2}):(\d{2})/.exec(String(value))
    if (!match) return
    preset.value.hour = Math.min(23, Number(match[1]))
    preset.value.minute = Math.min(59, Number(match[2]))
  },
})

function setKind(kind: string) {
  // Switching to custom starts from the current expression.
  if (kind === 'custom') preset.value.custom = expression.value
  preset.value.kind = kind as Preset['kind']
}

function setNumber(key: 'every' | 'minute' | 'day', value: string | number, min: number, max: number) {
  const n = Math.trunc(Number(value))
  preset.value[key] = Number.isFinite(n) ? Math.min(max, Math.max(min, n)) : min
}

async function check(expr: string) {
  controller?.abort()
  if (!isScheduleText(expr)) {
    info.value = null
    checkError.value = expr.trim() ? t('errors.validation.rule.schedule', { field: t('common.field.schedule') }) : null
    return
  }
  const request = new AbortController()
  controller = request
  checking.value = true
  try {
    info.value = await checkSchedule(expr, request.signal)
    checkError.value = null
  } catch (err) {
    if (isAbortError(err)) return
    info.value = null
    checkError.value = err
  } finally {
    if (controller === request) checking.value = false
  }
}

watch(
  expression,
  (expr) => {
    clearTimeout(timer)
    timer = setTimeout(() => void check(expr), CHECK_DELAY_MS)
  },
  { immediate: true },
)

onBeforeUnmount(() => {
  clearTimeout(timer)
  controller?.abort()
})

const errorText = computed(() =>
  typeof checkError.value === 'string' ? checkError.value : checkError.value ? errorMessage(checkError.value) : '',
)
/** What systemd-analyze said about a schedule it could not read. */
const errorDetail = computed(() => {
  const detail = isApiError(checkError.value) ? checkError.value.params.detail : undefined
  return typeof detail === 'string' ? detail : ''
})

const baseId = computed(() => props.id ?? 'schedule')

defineExpose({
  /** The server accepted the current expression. */
  valid: () => info.value !== null && !checking.value,
})
</script>

<template>
  <div class="flex min-w-0 flex-col gap-2">
    <div class="flex flex-wrap items-center gap-2">
      <SelectField
        :id="baseId"
        :model-value="preset.kind"
        :options="kindOptions"
        :aria-invalid="invalid || undefined"
        class="w-full sm:w-52"
        @update:model-value="setKind"
      />
      <label v-if="preset.kind === 'minutes'" class="inline-flex items-center gap-2 text-detail">
        <span class="sr-only">{{ t('cron.schedule.every') }}</span>
        <Input
          :model-value="String(preset.every)"
          type="number"
          min="1"
          max="59"
          inputmode="numeric"
          class="w-20 tabular-nums"
          @update:model-value="(value: string | number) => setNumber('every', value, 1, 59)"
        />
        {{ t('cron.schedule.minutesUnit') }}
      </label>
      <label v-else-if="preset.kind === 'hourly'" class="inline-flex items-center gap-2 text-detail">
        {{ t('cron.schedule.atMinute') }}
        <Input
          :model-value="String(preset.minute)"
          type="number"
          min="0"
          max="59"
          inputmode="numeric"
          class="w-20 tabular-nums"
          @update:model-value="(value: string | number) => setNumber('minute', value, 0, 59)"
        />
      </label>
      <template v-else-if="preset.kind !== 'custom'">
        <SelectField
          v-if="preset.kind === 'weekly'"
          v-model="preset.weekday"
          :options="weekdayOptions"
          :aria-label="t('cron.schedule.weekday')"
          class="w-36"
        />
        <label v-if="preset.kind === 'monthly'" class="inline-flex items-center gap-2 text-detail">
          {{ t('cron.schedule.day') }}
          <Input
            :model-value="String(preset.day)"
            type="number"
            min="1"
            max="31"
            inputmode="numeric"
            class="w-20 tabular-nums"
            @update:model-value="(value: string | number) => setNumber('day', value, 1, 31)"
          />
        </label>
        <Input v-model="time" type="time" step="60" :aria-label="t('cron.schedule.time')" class="w-32 tabular-nums" />
      </template>
    </div>
    <Input
      v-if="preset.kind === 'custom'"
      v-model="preset.custom"
      :aria-label="t('cron.schedule.expression')"
      :aria-invalid="invalid || !!errorText || undefined"
      autocomplete="off"
      spellcheck="false"
      autocapitalize="none"
      class="font-mono"
    />
    <p v-if="preset.kind === 'custom'" class="m-0 text-xs text-muted-foreground">{{ t('cron.schedule.customHint') }}</p>
    <p v-if="preset.kind === 'monthly' && preset.day > 28" class="m-0 text-xs text-muted-foreground">{{ t('cron.schedule.shortMonths') }}</p>

    <div v-if="errorText" role="alert" class="rounded-md bg-destructive-soft px-3 py-2 text-xs font-medium text-destructive">
      {{ errorText }}
      <pre v-if="errorDetail" class="mt-1 mb-0 font-mono font-normal whitespace-pre-wrap [overflow-wrap:anywhere]" dir="ltr">{{ errorDetail }}</pre>
    </div>
    <div v-else class="flex flex-col gap-1 rounded-md bg-muted px-3 py-2 text-xs text-muted-foreground" role="status">
      <span v-if="checking && !info" class="inline-flex items-center gap-1.5">
        <LoaderCircle class="size-3.5 animate-spin" aria-hidden="true" />
        {{ t('cron.schedule.checking') }}
      </span>
      <template v-else-if="info">
        <span class="inline-flex flex-wrap items-center gap-1.5">
          <CalendarClock class="size-3.5" aria-hidden="true" />
          {{ t('cron.schedule.normalized') }}
          <span class="font-mono text-foreground" dir="ltr">{{ info.normalized }}</span>
        </span>
        <span v-if="info.next.length > 0">{{ t('cron.schedule.next', { times: info.next.map(formatDateTime).join(t('cron.schedule.timesSeparator')) }) }}</span>
      </template>
    </div>
  </div>
</template>
