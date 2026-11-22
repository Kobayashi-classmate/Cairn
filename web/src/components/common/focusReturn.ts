// SPDX-License-Identifier: AGPL-3.0-only
//
// Where keyboard focus goes back to when a dialog closes. Dialogs opened from
// a dropdown menu item would otherwise return focus to an item that closed
// with its menu, leaving focus on the page body.

/** The element focus should return to when a dialog opened now closes. */
export function focusReturnTarget(): HTMLElement | null {
  let target: Element | null = document.activeElement
  // A menu item: return to the button that opened the menu (Reka labels a
  // menu with its trigger), also from a submenu.
  for (let depth = 0; depth < 4 && target; depth++) {
    const menu = target.closest('[role="menu"]')
    if (!menu) break
    const triggerId = menu.getAttribute('aria-labelledby')
    target = triggerId ? document.getElementById(triggerId) : null
  }
  return target instanceof HTMLElement && target !== document.body ? target : null
}

/** Moves focus back to `target` when it is still on the page; true when it did. */
export function returnFocus(target: HTMLElement | null): boolean {
  if (!target?.isConnected) return false
  target.focus()
  return document.activeElement === target
}
