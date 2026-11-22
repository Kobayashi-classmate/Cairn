// SPDX-License-Identifier: AGPL-3.0-only
//
// What one account's pages loaded goes when it signs out or its session
// ends, so another account signing in on this tab never sees it (lists,
// scripts, cached users, running uploads). Each store that holds such data
// registers how to forget it when it is created; the session store runs
// them all.

const resets = new Set<() => void>()

/** Registers how a store forgets an account's data (call it in the store's setup). */
export function onAccountReset(reset: () => void): void {
  resets.add(reset)
}

/** Forgets every account's data the stores hold. */
export function resetAccountData(): void {
  for (const reset of resets) {
    try {
      reset()
    } catch (err) {
      console.warn('cairn: resetting a store failed', err)
    }
  }
}
