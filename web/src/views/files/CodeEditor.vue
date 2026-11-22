<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// CodeMirror 6 editor. Loaded on demand, so the editor stays out of the
// main bundle. The document lives in the editor; the parent reads it with
// the exposed getValue() and hears about changes through `changed`.
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { basicSetup } from 'codemirror'
import { indentWithTab } from '@codemirror/commands'
import { Annotation, Compartment, EditorState } from '@codemirror/state'
import { EditorView, keymap } from '@codemirror/view'
import { defaultHighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { oneDarkHighlightStyle } from '@codemirror/theme-one-dark'
import { languageFor } from '@/files/languages'
import { useThemeStore } from '@/stores/theme'

const props = defineProps<{
  /** Initial text; later changes replace the document. */
  value: string
  /** File path, for picking the highlighting. */
  path: string
  readOnly?: boolean
  /** Line to show and put the cursor on (1-based). */
  line?: number
  /** False keeps the focus where it is (an editor inside a form). */
  autofocus?: boolean
  /** Accessible name of the text area; the file path when not given. */
  label?: string
}>()
const emit = defineEmits<{ changed: []; save: [] }>()

const host = ref<HTMLElement | null>(null)
// Follows the panel's light/dark mode (which may differ from the system's).
const panelTheme = useThemeStore()
const language = new Compartment()
const theme = new Compartment()
const editable = new Compartment()
/** Marks document replacements from `value`, which are not edits. */
const external = Annotation.define<boolean>()
let view: EditorView | null = null

const baseTheme = EditorView.theme({
  '&': { height: '100%', fontSize: '13px' },
  '.cm-scroller': { fontFamily: 'var(--font-mono, ui-monospace, monospace)' },
})

/**
 * The editor's colours come from the panel's tokens (interface spec,
 * section 3), which switch with the light and dark mode; only the syntax
 * colours have a light and a dark set.
 */
const chrome = {
  '&': { color: 'var(--foreground)', backgroundColor: 'var(--card)' },
  '.cm-content': { caretColor: 'var(--foreground)' },
  '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--foreground)' },
  '&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection':
    { backgroundColor: 'color-mix(in srgb, var(--primary) 22%, transparent)' },
  '.cm-activeLine': { backgroundColor: 'color-mix(in srgb, var(--accent) 70%, transparent)' },
  '.cm-gutters': { backgroundColor: 'var(--muted)', color: 'var(--muted-foreground)', borderRight: '1px solid var(--border)' },
  '.cm-activeLineGutter': { backgroundColor: 'var(--accent)', color: 'var(--foreground)' },
  '.cm-foldPlaceholder': { backgroundColor: 'var(--muted)', border: '1px solid var(--border)', color: 'var(--muted-foreground)' },
  '.cm-tooltip': { backgroundColor: 'var(--popover)', color: 'var(--popover-foreground)', border: '1px solid var(--border)' },
  '.cm-panels': { backgroundColor: 'var(--muted)', color: 'var(--foreground)' },
  '.cm-panels.cm-panels-top': { borderBottom: '1px solid var(--border)' },
  '.cm-panels.cm-panels-bottom': { borderTop: '1px solid var(--border)' },
  '.cm-searchMatch': { backgroundColor: 'color-mix(in srgb, var(--warning) 30%, transparent)' },
  '.cm-searchMatch.cm-searchMatch-selected': { backgroundColor: 'color-mix(in srgb, var(--primary) 35%, transparent)' },
  '.cm-matchingBracket, .cm-nonmatchingBracket': { backgroundColor: 'color-mix(in srgb, var(--primary) 20%, transparent)' },
}
const lightTheme = [EditorView.theme(chrome, { dark: false }), syntaxHighlighting(defaultHighlightStyle)]
const darkTheme = [EditorView.theme(chrome, { dark: true }), syntaxHighlighting(oneDarkHighlightStyle)]

function themeFor(dark: boolean) {
  return dark ? darkTheme : lightTheme
}

function goToLine(line: number | undefined) {
  if (!view || !line) return
  const doc = view.state.doc
  const target = doc.line(Math.min(Math.max(line, 1), doc.lines))
  view.dispatch({ selection: { anchor: target.from }, effects: EditorView.scrollIntoView(target.from, { y: 'center' }) })
}

async function loadLanguage(path: string) {
  const extension = await languageFor(path).catch(() => null)
  view?.dispatch({ effects: language.reconfigure(extension ?? []) })
}

onMounted(() => {
  if (!host.value) return
  view = new EditorView({
    parent: host.value,
    state: EditorState.create({
      doc: props.value,
      extensions: [
        basicSetup,
        keymap.of([
          {
            key: 'Mod-s',
            preventDefault: true,
            run: () => {
              emit('save')
              return true
            },
          },
          indentWithTab,
        ]),
        language.of([]),
        theme.of(themeFor(panelTheme.isDark)),
        editable.of([EditorState.readOnly.of(!!props.readOnly)]),
        baseTheme,
        // The editable area is a text box for screen readers; give it a name.
        // tabindex makes it count as the focusable content of its scroller.
        EditorView.contentAttributes.of({ 'aria-label': props.label || props.path, tabindex: '0' }),
        EditorView.updateListener.of((update) => {
          if (update.docChanged && !update.transactions.some((tr) => tr.annotation(external))) emit('changed')
        }),
      ],
    }),
  })
  goToLine(props.line)
  if (props.autofocus !== false) view.focus()
  void loadLanguage(props.path)
})

onBeforeUnmount(() => {
  view?.destroy()
  view = null
})

watch(
  () => panelTheme.isDark,
  (dark) => view?.dispatch({ effects: theme.reconfigure(themeFor(dark)) }),
)
watch(
  () => props.readOnly,
  (value) => view?.dispatch({ effects: editable.reconfigure([EditorState.readOnly.of(!!value)]) }),
)
watch(
  () => props.value,
  (value) => {
    if (!view || value === view.state.doc.toString()) return
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: value }, annotations: external.of(true) })
  },
)
watch(
  () => props.path,
  (path) => void loadLanguage(path),
)
watch(
  () => props.line,
  (line) => goToLine(line),
)

defineExpose({
  getValue: (): string => view?.state.doc.toString() ?? props.value,
  focus: () => view?.focus(),
})
</script>

<template>
  <div ref="host" class="code-editor" dir="ltr" />
</template>

<style scoped>
.code-editor {
  block-size: 100%;
  min-block-size: 0;
  border: 1px solid var(--border);
  border-radius: calc(var(--radius) - 2px);
  overflow: hidden;
}

.code-editor :deep(.cm-editor) {
  block-size: 100%;
}

.code-editor :deep(.cm-editor.cm-focused) {
  outline: none;
}
</style>
