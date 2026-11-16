// SPDX-License-Identifier: AGPL-3.0-only
//
// Syntax highlighting for the editor, picked by file name. Each language
// pack is loaded only when a file needs it.
import type { StreamParser } from '@codemirror/language'
import type { Extension } from '@codemirror/state'
import { extensionOf } from './paths'

type Loader = () => Promise<Extension>

/** A CodeMirror 5 mode from @codemirror/legacy-modes. */
async function legacy(load: () => Promise<StreamParser<unknown>>): Promise<Extension> {
  const [{ StreamLanguage }, mode] = await Promise.all([import('@codemirror/language'), load()])
  return StreamLanguage.define(mode)
}

const javascript: Loader = async () => (await import('@codemirror/lang-javascript')).javascript()
const typescript: Loader = async () => (await import('@codemirror/lang-javascript')).javascript({ typescript: true })
const jsx: Loader = async () => (await import('@codemirror/lang-javascript')).javascript({ jsx: true })
const tsx: Loader = async () => (await import('@codemirror/lang-javascript')).javascript({ jsx: true, typescript: true })
const css: Loader = async () => (await import('@codemirror/lang-css')).css()
const html: Loader = async () => (await import('@codemirror/lang-html')).html()
const php: Loader = async () => (await import('@codemirror/lang-php')).php()
const python: Loader = async () => (await import('@codemirror/lang-python')).python()
const json: Loader = async () => (await import('@codemirror/lang-json')).json()
const yaml: Loader = async () => (await import('@codemirror/lang-yaml')).yaml()
const markdown: Loader = async () => (await import('@codemirror/lang-markdown')).markdown()
const sql: Loader = async () => (await import('@codemirror/lang-sql')).sql()
const xml: Loader = async () => (await import('@codemirror/lang-xml')).xml()
const shell: Loader = () => legacy(async () => (await import('@codemirror/legacy-modes/mode/shell')).shell)
const nginx: Loader = () => legacy(async () => (await import('@codemirror/legacy-modes/mode/nginx')).nginx)
const properties: Loader = () => legacy(async () => (await import('@codemirror/legacy-modes/mode/properties')).properties)
const toml: Loader = () => legacy(async () => (await import('@codemirror/legacy-modes/mode/toml')).toml)
const dockerfile: Loader = () => legacy(async () => (await import('@codemirror/legacy-modes/mode/dockerfile')).dockerFile)

const BY_EXTENSION: Record<string, Loader> = {
  js: javascript,
  mjs: javascript,
  cjs: javascript,
  jsx,
  ts: typescript,
  mts: typescript,
  cts: typescript,
  tsx,
  css,
  scss: css,
  less: css,
  html,
  htm: html,
  vue: html,
  php,
  phtml: php,
  py: python,
  json,
  webmanifest: json,
  yaml,
  yml: yaml,
  md: markdown,
  markdown,
  sql,
  xml,
  svg: xml,
  xsl: xml,
  xsd: xml,
  sh: shell,
  bash: shell,
  zsh: shell,
  ini: properties,
  cfg: properties,
  env: properties,
  properties,
  cnf: properties,
  toml,
}

const BY_NAME: Record<string, Loader> = {
  '.bashrc': shell,
  '.bash_profile': shell,
  '.profile': shell,
  '.zshrc': shell,
  '.env': properties,
  '.user.ini': properties,
  dockerfile,
  'nginx.conf': nginx,
}

/** Highlighting for a file, or null for plain text. */
export async function languageFor(path: string): Promise<Extension | null> {
  const name = (path.slice(path.lastIndexOf('/') + 1) || path).toLowerCase()
  const byName = BY_NAME[name]
  if (byName) return byName()
  const ext = extensionOf(name)
  // Server configuration: nginx files sit in nginx directories, the rest reads like ini.
  if (ext === 'conf') return /(^|\/)nginx(\/|$)|vhost/.test(path.toLowerCase()) ? nginx() : properties()
  const loader = BY_EXTENSION[ext]
  return loader ? loader() : null
}
