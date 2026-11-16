<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Picks a schedule from presets or as a raw OnCalendar expression, and
// shows how systemd reads it with the next runs.
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NInput, NInputNumber, NSelect, NText, NTimePicker } from 'naive-ui'
import { isAbortError, isApiError } from '@/api/client'
import { checkSchedule, type ScheduleInfo } from '@/api/cron'
import { PRESET_KINDS, WEEKDAYS, fromExpression, isScheduleText, toExpression, type Preset } from '@/cron/schedule'
import { errorMessage } from '@/i18n/errors'
import { formatDateTime } from '@/i18n/format'

const expression = defineModel<string>('expression', { required: true })

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

const kindOptions = computed(() => PRESET_KINDS.map((kind) => ({ value: kind, label: t(`cron.schedule.kind.${kind}`) })))
const weekdayOptions = computed(() => WEEKDAYS.map((day) => ({ value: day, label: t(`cron.weekday.${day}`) })))

/** The time of day as the time picker wants it (milliseconds into a day, local). */
const timeValue = computed({
  get: () => new Date(2000, 0, 1, preset.value.hour, preset.value.minute).getTime(),
  set: (value: number | null) => {
    if (value === null) return
    const date = new Date(value)
    preset.value.hour = date.getHours()
    preset.value.minute = date.getMinutes()
  },
})

function setKind(kind: Preset['kind']) {
  // Switching to custom starts from the current expression.
  if (kind === 'custom') preset.value.custom = expression.value
  preset.value.kind = kind
}

async function check(expr: string) {
  controller?.abort()
  if (!isScheduleText(expr)) {
    info.value = null
    checkError.value = expr.trim() ? t('errors.validation.rule.schedule', { field: t('common.field.schedule') }) : null
    return
  }
  const current = new AbortController()
  controller = current
  checking.value = true
  try {
    info.value = await checkSchedule(expr, current.signal)
    checkError.value = null
  } catch (err) {
    if (isAbortError(err)) return
    info.value = null
    checkError.value = err
  } finally {
    if (controller === current) checking.value = false
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

const errorText = computed(() => (typeof checkError.value === 'string' ? checkError.value : checkError.value ? errorMessage(checkError.value) : ''))
/** What systemd-analyze said about a schedule it could not read. */
const errorDetail = computed(() => {
  const detail = isApiError(checkError.value) ? checkError.value.params.detail : undefined
  return typeof detail === 'string' ? detail : ''
})

defineExpose({
  /** The server accepted the current expression. */
  valid: () => info.value !== null && !checking.value,
})
</script>

<template>
  <div class="schedule">
    <div class="schedule__row">
      <NSelect :value="preset.kind" :options="kindOptions" class="schedule__kind" @update:value="setKind" />
      <template v-if="preset.kind === 'minutes'">
        <NInputNumber v-model:value="preset.every" :min="1" :max="59" :precision="0" class="schedule__number">
          <template #suffix>{{ t('cron.schedule.minutesUnit') }}</template>
        </NInputNumber>
      </template>
      <template v-else-if="preset.kind === 'hourly'">
        <NInputNumber v-model:value="preset.minute" :min="0" :max="59" :precision="0" class="schedule__number">
          <template #prefix>{{ t('cron.schedule.atMinute') }}</template>
        </NInputNumber>
      </template>
      <template v-else-if="preset.kind !== 'custom'">
        <NSelect v-if="preset.kind === 'weekly'" v-model:value="preset.weekday" :options="weekdayOptions" class="schedule__weekday" />
        <NInputNumber
          v-if="preset.kind === 'monthly'"
          v-model:value="preset.day"
          :min="1"
          :max="31"
          :precision="0"
          class="schedule__number"
        >
          <template #prefix>{{ t('cron.schedule.day') }}</template>
        </NInputNumber>
        <NTimePicker v-model:value="timeValue" format="HH:mm" :actions="null" class="schedule__time" />
      </template>
      <NInput
        v-else
        v-model:value="preset.custom"
        class="schedule__custom mono"
        :input-props="{ autocomplete: 'off', spellcheck: false, 'aria-label': t('common.field.schedule') }"
      />
    </div>
    <NText v-if="preset.kind === 'custom'" depth="3" tag="p" class="schedule__hint">{{ t('cron.schedule.customHint') }}</NText>
    <NText v-if="preset.kind === 'monthly' && preset.day > 28" depth="3" tag="p" class="schedule__hint">
      {{ t('cron.schedule.shortMonths') }}
    </NText>
    <NAlert v-if="errorText" type="error" :show-icon="false" class="schedule__result">
      {{ errorText }}
      <pre v-if="errorDetail" class="schedule__detail" dir="ltr">{{ errorDetail }}</pre>
    </NAlert>
    <div v-else-if="info" class="schedule__result">
      <NText depth="3" class="schedule__line">
        {{ t('cron.schedule.normalized') }} <span class="mono" dir="ltr">{{ info.normalized }}</span>
      </NText>
      <NText v-if="info.next.length > 0" depth="3" class="schedule__line">
        {{ t('cron.schedule.next', { times: info.next.map(formatDateTime).join(', ') }) }}
      </NText>
    </div>
  </div>
</template>

<style scoped>
.schedule {
  display: flex;
  flex-direction: column;
  gap: 6px;
  inline-size: 100%;
}

.schedule__row {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.schedule__kind {
  inline-size: 180px;
}

.schedule__number {
  inline-size: 150px;
}

.schedule__weekday {
  inline-size: 140px;
}

.schedule__time {
  inline-size: 120px;
}

.schedule__custom {
  flex: 1 1 240px;
}

.schedule__hint {
  margin: 0;
  font-size: 12px;
}

.schedule__result {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.schedule__detail {
  margin: 4px 0 0;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.schedule__line {
  font-size: 12px;
}

.mono,
.mono :deep(input) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}
</style>
