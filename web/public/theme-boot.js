// SPDX-License-Identifier: AGPL-3.0-only
//
// Applies the colour scheme remembered in this browser before the first
// paint, so a dark page does not flash light while the app loads. It is a
// separate file because the panel's Content-Security-Policy does not allow
// inline scripts. The app (src/stores/theme.ts) takes over once it starts;
// keep the storage key and the values in step with it.
;(function () {
  var mode = 'system'
  try {
    var stored = window.localStorage.getItem('cairn.theme')
    if (stored === 'light' || stored === 'dark' || stored === 'system') mode = stored
  } catch (e) {
    // Storage unavailable: follow the system.
  }
  var dark = mode === 'dark'
  if (mode === 'system') {
    try {
      dark = window.matchMedia('(prefers-color-scheme: dark)').matches
    } catch (e) {
      dark = false
    }
  }
  var root = document.documentElement
  if (dark) root.classList.add('dark')
  else root.classList.remove('dark')
  root.style.colorScheme = dark ? 'dark' : 'light'
})()
