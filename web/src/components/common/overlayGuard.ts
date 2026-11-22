// SPDX-License-Identifier: AGPL-3.0-only
//
// Sheets, drawers and dialogs built on Reka close when the user interacts
// outside them or presses Escape. Some layers outside them are part of the
// same task and must not close them: the re-authentication dialog (opened by
// the API client in the middle of a request), toasts and the job dock. Bind
// these handlers on Reka's DialogContent / AlertDialogContent.
import { onBeforeUnmount, ref, watch } from 'vue'
import { stepUpOpen } from '@/auth/stepUp'

/** Layers that may be used while a Reka dialog stays open (see also styles/main.css). */
const COMPANION_LAYERS = '[data-sonner-toaster], [data-job-dock]'

interface OutsideEvent {
  target: EventTarget | null
  detail?: { originalEvent?: Event }
  preventDefault(): void
}

/** For `@interact-outside`: keep the dialog open while re-authenticating or using a companion layer. */
export function keepOpenOnCompanion(event: OutsideEvent): void {
  if (stepUpOpen.value) {
    event.preventDefault()
    return
  }
  const target = event.detail?.originalEvent?.target ?? event.target
  if (target instanceof Element && target.closest(COMPANION_LAYERS)) event.preventDefault()
}

/** For `@escape-key-down`: Escape then belongs to the re-authentication dialog. */
export function keepOpenOnEscape(event: KeyboardEvent): void {
  if (stepUpOpen.value) event.preventDefault()
}

/** How many sheets, drawers and dialogs built on Reka are open; the job dock moves aside meanwhile. */
export const openOverlays = ref(0)

/** Counts a sheet or dialog as open while `open` is true (and it is mounted). */
export function trackOverlay(open: () => boolean): void {
  let counted = false
  const update = (value: boolean) => {
    if (value === counted) return
    counted = value
    openOverlays.value += value ? 1 : -1
  }
  watch(open, update, { immediate: true })
  onBeforeUnmount(() => update(false))
}
