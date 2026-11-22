// SPDX-License-Identifier: AGPL-3.0-only
//
// The Terminal page's saved commands: the account's own list, shared by the
// side panel (or its sheet on narrow screens) and the add/edit sheet.
import { defineStore } from 'pinia'
import { ref } from 'vue'
import { deleteSnippet, fetchSnippets, type TermSnippet } from '@/api/terminal'

function byName(a: TermSnippet, b: TermSnippet): number {
  return a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }) || a.id - b.id
}

export const useTerminalStore = defineStore('terminal', () => {
  const snippets = ref<TermSnippet[] | null>(null)
  const error = ref<unknown>(null)
  const loading = ref(false)

  /** Numbers the loads, so an older answer arriving late is dropped. */
  let seq = 0

  async function load(): Promise<void> {
    const mine = ++seq
    loading.value = true
    try {
      const list = await fetchSnippets()
      if (mine !== seq) return
      snippets.value = list
      error.value = null
    } catch (err) {
      if (mine === seq) error.value = err
    } finally {
      if (mine === seq) loading.value = false
    }
  }

  /** Puts a created or changed command into the list, in name order. */
  function put(snippet: TermSnippet): void {
    const rest = (snippets.value ?? []).filter((item) => item.id !== snippet.id)
    snippets.value = [...rest, snippet].sort(byName)
  }

  async function remove(snippet: TermSnippet): Promise<void> {
    await deleteSnippet(snippet.id)
    snippets.value = (snippets.value ?? []).filter((item) => item.id !== snippet.id)
  }

  /** Forgets the list (the page is left; another account may sign in on this tab). */
  function clear(): void {
    seq++
    snippets.value = null
    error.value = null
    loading.value = false
  }

  return { snippets, error, loading, load, put, remove, clear }
})
