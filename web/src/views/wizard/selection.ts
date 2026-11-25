// SPDX-License-Identifier: AGPL-3.0-only
//
// The software selection of the first-login wizard: one line per software.
// Presets (suites) are starting points only. Applying one adds what the
// selection does not cover yet, in the recommended line, and never changes
// a row that is already there; every row stays the user's to change or
// remove.
import { MAX_PROBE_ITEMS, isAvailable, type Offer, type SoftwareEntry, type Suite, type SuiteItem } from '@/api/software'

/** One row of the selection: a software and the line to install. */
export interface PickedLine {
  software: string
  line: string
}

export type EntryMap = ReadonlyMap<string, SoftwareEntry>

/** A line that can be installed here and is not installed yet. */
export function canChoose(offer: Offer): boolean {
  return isAvailable(offer) && !offer.installed
}

/**
 * The line a software is added with: the recommended one, else the newest
 * line still supported upstream, else the newest one at all; "" when every
 * installable line is installed already.
 */
export function defaultLine(entry: SoftwareEntry): string {
  const open = entry.offers.filter(canChoose)
  return (open.find((o) => o.recommended) ?? open.find((o) => !o.endOfLife) ?? open[0])?.line ?? ''
}

/** The software has a line that could still be added to the selection. */
export function canAdd(entry: SoftwareEntry | undefined): entry is SoftwareEntry {
  return entry !== undefined && defaultLine(entry) !== ''
}

export function offerOf(entries: EntryMap, software: string, line: string): Offer | undefined {
  return entries.get(software)?.offers.find((o) => o.line === line)
}

function hasInstalledLine(entry: SoftwareEntry | undefined): boolean {
  return entry?.offers.some((o) => o.installed) === true
}

const picks = (picked: readonly PickedLine[], software: string) => picked.some((p) => p.software === software)

/** How far the selection covers one preset item. */
export type ItemState = 'missing' | 'picked' | 'installed' | 'open'

/**
 * A preset item is covered when one of its choices is in the selection
 * (in any line) or has a line installed already: Valkey covers "Redis or
 * Valkey", an installed PHP 8.3 covers "PHP". An item none of whose
 * choices can be added counts as missing. `entries` holds the whole
 * catalog, so that installed lines that can no longer be installed count.
 */
export function itemState(item: SuiteItem, picked: readonly PickedLine[], entries: EntryMap): ItemState {
  if (item.missing) return 'missing'
  if (item.choices.some((id) => picks(picked, id))) return 'picked'
  if (item.choices.some((id) => hasInstalledLine(entries.get(id)))) return 'installed'
  return item.choices.some((id) => canAdd(entries.get(id))) ? 'open' : 'missing'
}

/** How far the selection covers a whole preset. */
export type PresetState = 'included' | 'installed' | 'open' | 'unavailable'

export function presetState(suite: Suite, picked: readonly PickedLine[], entries: EntryMap): PresetState {
  const states = suite.items.map((item) => itemState(item, picked, entries)).filter((s) => s !== 'missing')
  if (states.length === 0) return 'unavailable'
  if (states.includes('open')) return 'open'
  return states.includes('picked') ? 'included' : 'installed'
}

/**
 * The software an uncovered preset item adds: the one the server resolved
 * (the first choice with a recommended line here), else the first choice
 * that can still be added.
 */
function presetChoice(item: SuiteItem, picked: readonly PickedLine[], entries: EntryMap): SoftwareEntry | undefined {
  const order = item.software ? [item.software, ...item.choices] : item.choices
  return order.map((id) => entries.get(id)).find((entry) => canAdd(entry) && !picks(picked, entry.id))
}

/**
 * Adds a preset's uncovered items to the selection, each in its default
 * line, up to MAX_PROBE_ITEMS rows. `left` counts the items that did not
 * fit.
 */
export function applyPreset(
  suite: Suite,
  picked: readonly PickedLine[],
  entries: EntryMap,
): { picked: PickedLine[]; left: number } {
  const next = [...picked]
  let left = 0
  for (const item of suite.items) {
    if (itemState(item, next, entries) !== 'open') continue
    const entry = presetChoice(item, next, entries)
    if (!entry) continue
    if (next.length >= MAX_PROBE_ITEMS) {
      left++
      continue
    }
    next.push({ software: entry.id, line: defaultLine(entry) })
  }
  return { picked: next, left }
}

/**
 * Software that can take the place of a selected one, from the presets'
 * lists of choices (Valkey for Redis and back), not selected yet and with
 * a line that can be added. Empty for software without alternatives.
 */
export function alternativesOf(
  software: string,
  suites: readonly Suite[],
  picked: readonly PickedLine[],
  entries: EntryMap,
): string[] {
  const out = new Set<string>()
  for (const suite of suites) {
    for (const item of suite.items) {
      if (!item.choices.includes(software)) continue
      for (const id of item.choices) {
        if (id !== software && !picks(picked, id) && canAdd(entries.get(id))) out.add(id)
      }
    }
  }
  return [...out]
}
