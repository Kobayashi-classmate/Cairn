<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { nextTick, ref, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import { ListChecks, LoaderCircle, X } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { Sheet, SheetClose, SheetContent, SheetDescription, SheetTitle } from '@/components/ui/sheet'
import { keepOpenOnCompanion, keepOpenOnEscape, trackOverlay } from './overlayGuard'

// The narrow sheet for creating things (interface spec, section 6): 460px
// from the right, full screen on phones, the form in the middle and a fixed
// footer with "Cancel" and the action. Changes to the system list the steps
// they will run ("Will do") above the footer.
const props = withDefaults(
  defineProps<{
    title: string
    description?: string
    /** The action's verb ("Create site"). */
    submitLabel: string
    /** The request is on its way: the buttons wait. */
    submitting?: boolean
    submitDisabled?: boolean
    /** What the action will do to the system, one step per entry. */
    steps?: readonly string[]
  }>(),
  { description: undefined, steps: () => [] },
)

const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ submit: [] }>()

const { t } = useI18n()
trackOverlay(() => open.value)
const formId = `form-sheet-${useId()}`
const body = ref<HTMLFormElement | null>(null)

/** Focus the first field instead of the close button. */
function onOpenAutoFocus(event: Event) {
  event.preventDefault()
  void nextTick(() => {
    const field = body.value?.querySelector<HTMLElement>(
      'input:not([type=hidden]):not(:disabled), textarea:not(:disabled), select:not(:disabled), button[role=radio]:not(:disabled), button[role=combobox]:not(:disabled)',
    )
    field?.focus()
  })
}

function onSubmit() {
  if (props.submitting || props.submitDisabled) return
  emit('submit')
}
</script>

<template>
  <Sheet v-model:open="open">
    <SheetContent
      side="right"
      :show-close-button="false"
      class="h-dvh w-full max-w-none gap-0 p-0 sm:w-[460px] sm:max-w-[460px]"
      @open-auto-focus="onOpenAutoFocus"
      @interact-outside="keepOpenOnCompanion"
      @escape-key-down="keepOpenOnEscape"
    >
      <header class="flex items-start gap-3 border-b px-5 py-4">
        <div class="flex min-w-0 flex-1 flex-col gap-0.5">
          <SheetTitle class="text-base font-semibold">{{ title }}</SheetTitle>
          <SheetDescription v-if="description" class="text-detail text-muted-foreground">
            {{ description }}
          </SheetDescription>
          <SheetDescription v-else class="sr-only">{{ title }}</SheetDescription>
        </div>
        <SheetClose as-child>
          <Button variant="ghost" size="icon" class="-me-1.5 max-md:size-11" :aria-label="t('common.action.close')">
            <X aria-hidden="true" />
          </Button>
        </SheetClose>
      </header>

      <form
        :id="formId"
        ref="body"
        class="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto px-5 py-5"
        novalidate
        @submit.prevent="onSubmit"
      >
        <slot />

        <section v-if="steps.length > 0" class="flex flex-col gap-2" :aria-label="t('common.sheet.willDo')">
          <h3 class="m-0 flex items-center gap-1.5 text-detail font-semibold">
            <ListChecks class="size-4 text-muted-foreground" aria-hidden="true" />
            {{ t('common.sheet.willDo') }}
          </h3>
          <ol class="m-0 flex list-decimal flex-col gap-1.5 rounded-md bg-muted py-3 ps-8 pe-4 text-detail text-muted-foreground">
            <li v-for="(step, index) in steps" :key="index" class="[overflow-wrap:anywhere]">{{ step }}</li>
          </ol>
        </section>
      </form>

      <footer class="flex flex-wrap items-center justify-end gap-2 border-t px-5 py-3.5">
        <slot name="footer-start" />
        <SheetClose as-child>
          <Button variant="outline" class="max-md:h-11">{{ t('common.action.cancel') }}</Button>
        </SheetClose>
        <Button type="submit" :form="formId" :disabled="submitting || submitDisabled" class="max-md:h-11">
          <LoaderCircle v-if="submitting" class="animate-spin" aria-hidden="true" />
          {{ submitLabel }}
        </Button>
      </footer>
    </SheetContent>
  </Sheet>
</template>
