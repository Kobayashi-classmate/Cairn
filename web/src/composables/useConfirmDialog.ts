// SPDX-License-Identifier: AGPL-3.0-only
//
// Confirmations (interface spec, section 7): the dialog states the
// consequence, destructive actions use the danger colour, and deleting a
// site asks for its name. ConfirmHost (in the shell) renders the request.
import { shallowRef } from 'vue'

export interface ConfirmOptions {
  title: string
  /** The consequence, in one or two sentences; may depend on `option`. */
  description: string | ((optionChecked: boolean) => string)
  /** Text shown as is under the description, in monospace (an address, a path). */
  detail?: string
  /** The verb on the confirming button ("Delete site"); may depend on `option`. */
  confirmLabel: string | ((optionChecked: boolean) => string)
  destructive?: boolean
  /** The user types this exact text (a site's primary domain) to enable the button. */
  typeToConfirm?: string
  /** An extra choice shown as a checkbox, such as "Also delete the files". */
  option?: { label: string; checked?: boolean }
  /**
   * Runs when confirmed. The dialog stays open with a busy button until it
   * settles; an error is shown in the dialog, which stays open. A declined
   * re-authentication keeps the dialog open without an error.
   */
  run?: (optionChecked: boolean) => Promise<void>
}

export interface ConfirmRequest {
  options: ConfirmOptions
  resolve: (confirmed: boolean) => void
}

/** The confirmation on screen, if any. */
export const confirmRequest = shallowRef<ConfirmRequest | null>(null)

/** Asks for confirmation; resolves true once confirmed (and `run` succeeded), false when dismissed. */
export function confirmAction(options: ConfirmOptions): Promise<boolean> {
  confirmRequest.value?.resolve(false)
  return new Promise<boolean>((resolve) => {
    confirmRequest.value = { options, resolve }
  })
}
