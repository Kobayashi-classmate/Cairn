// SPDX-License-Identifier: AGPL-3.0-only
//
// Sheets, drawers and dialogs share one z-index, so the one whose portal
// comes last in <body> is painted on top. A portal is placed when its
// component mounts, which is not the order they open in: the re-authentication
// dialog (mounted at startup) would sit under a drawer opened later, with the
// keyboard focus inside it. Keying the portal with this counter re-creates it
// at the end of <body> each time the dialog opens.
import { ref, type Ref, watch } from 'vue'
import { injectDialogRootContext } from 'reka-ui'

/** Changes each time the surrounding Reka dialog (or alert dialog, or sheet) opens. */
export function useOpenCount(): Ref<number> {
  const root = injectDialogRootContext()
  const count = ref(0)
  watch(
    () => root.open.value,
    (open) => {
      if (open) count.value++
    },
    { immediate: true },
  )
  return count
}
