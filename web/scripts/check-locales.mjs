// SPDX-License-Identifier: AGPL-3.0-only
//
// Checks the UI translations in src/locales/<locale>/<area>.json against the
// source locale (en):
//   - every locale has the same files and exactly the same message keys;
//   - each message uses the same {placeholder} names as its English source;
//   - every message is a non-empty string with well-formed placeholders;
//   - every locale directory is a valid BCP 47 language tag.
// Exits with status 1 and lists every problem when a check fails.
import { readdir, readFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const SOURCE_LOCALE = 'en'
const LOCALES_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../src/locales')

// vue-i18n named interpolation: {name}. Literal interpolation such as {'@'}
// is not a placeholder and is ignored.
const PLACEHOLDER = /\{\s*([A-Za-z_$][\w$]*)\s*\}/g
const LITERAL = /\{\s*'[^']*'\s*\}/g

const problems = []

function report(file, key, message) {
  problems.push(`${file}${key ? ` [${key}]` : ''}: ${message}`)
}

/** Flattens nested message objects into "a.b.c" keys. */
function flatten(tree, file, prefix, out) {
  for (const [name, value] of Object.entries(tree)) {
    const key = prefix ? `${prefix}.${name}` : name
    if (typeof value === 'string') {
      out.set(key, value)
    } else if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
      flatten(value, file, key, out)
    } else {
      report(file, key, 'value must be a string or an object')
    }
  }
}

function placeholders(message) {
  const names = new Set()
  for (const match of message.matchAll(PLACEHOLDER)) names.add(match[1])
  return [...names].sort()
}

/** Reports braces that are not part of a well-formed placeholder. */
function checkSyntax(file, key, message) {
  if (message.trim() === '') {
    report(file, key, 'message is empty')
    return
  }
  const rest = message.replace(LITERAL, '').replace(PLACEHOLDER, '')
  if (/[{}]/.test(rest)) {
    report(file, key, `malformed placeholder in "${message}"`)
  }
}

/** Loads every area file of a locale; returns Map<"area.key", message>. */
async function loadLocale(locale) {
  const dir = path.join(LOCALES_DIR, locale)
  const files = (await readdir(dir)).filter((name) => name.endsWith('.json')).sort()
  const messages = new Map()
  for (const name of files) {
    const file = path.join(locale, name)
    let tree
    try {
      tree = JSON.parse(await readFile(path.join(dir, name), 'utf8'))
    } catch (err) {
      report(file, '', `invalid JSON: ${err.message}`)
      continue
    }
    if (tree === null || typeof tree !== 'object' || Array.isArray(tree)) {
      report(file, '', 'top level must be an object')
      continue
    }
    const flat = new Map()
    flatten(tree, file, '', flat)
    const area = name.slice(0, -'.json'.length)
    for (const [key, message] of flat) {
      checkSyntax(file, key, message)
      messages.set(`${area}.${key}`, message)
    }
  }
  return { files, messages }
}

async function main() {
  const entries = await readdir(LOCALES_DIR, { withFileTypes: true })
  const locales = entries.filter((e) => e.isDirectory()).map((e) => e.name).sort()

  if (!locales.includes(SOURCE_LOCALE)) {
    console.error(`check-locales: source locale "${SOURCE_LOCALE}" not found in ${LOCALES_DIR}`)
    process.exit(1)
  }

  for (const locale of locales) {
    try {
      const [canonical] = Intl.getCanonicalLocales(locale)
      if (canonical !== locale) {
        report(locale, '', `directory name should be the canonical tag "${canonical}"`)
      }
    } catch {
      report(locale, '', 'directory name is not a valid BCP 47 language tag')
    }
  }

  const source = await loadLocale(SOURCE_LOCALE)
  let checkedKeys = 0

  for (const locale of locales) {
    if (locale === SOURCE_LOCALE) continue
    const target = await loadLocale(locale)

    for (const name of source.files) {
      if (!target.files.includes(name)) report(path.join(locale, name), '', 'file is missing')
    }
    for (const name of target.files) {
      if (!source.files.includes(name)) {
        report(path.join(locale, name), '', `file does not exist in ${SOURCE_LOCALE}`)
      }
    }

    for (const [key, sourceMessage] of source.messages) {
      const message = target.messages.get(key)
      const file = path.join(locale, `${key.split('.')[0]}.json`)
      const shortKey = key.slice(key.indexOf('.') + 1)
      if (message === undefined) {
        report(file, shortKey, 'key is missing')
        continue
      }
      checkedKeys++
      const want = placeholders(sourceMessage)
      const got = placeholders(message)
      if (want.join(',') !== got.join(',')) {
        report(
          file,
          shortKey,
          `placeholders {${got.join('}, {')}} differ from ${SOURCE_LOCALE} {${want.join('}, {')}}`,
        )
      }
    }
    for (const key of target.messages.keys()) {
      if (!source.messages.has(key)) {
        const file = path.join(locale, `${key.split('.')[0]}.json`)
        report(file, key.slice(key.indexOf('.') + 1), `key does not exist in ${SOURCE_LOCALE}`)
      }
    }
  }

  if (problems.length > 0) {
    console.error(`check-locales: ${problems.length} problem(s) found:`)
    for (const problem of problems) console.error(`  ${problem}`)
    process.exit(1)
  }
  console.log(
    `check-locales: OK (${locales.length} locales, ${source.messages.size} keys in ${SOURCE_LOCALE}, ${checkedKeys} translations checked)`,
  )
}

await main()
