// SPDX-License-Identifier: AGPL-3.0-only
//
// Which download source an installation uses, from a source probe: the
// preselected candidates and the `source` an install request carries. Shared
// by the install sheet and the first-login wizard.
import {
  SYSTEM_MAIN_SLOT,
  runtimeSlotId,
  type InstallMethod,
  type ProbeResult,
  type SourceCandidate,
  type SourceSlot,
} from '@/api/software'

/**
 * The slot whose candidate a line's installation takes as `source`: its
 * runtime download, or the system's main source for a system-method line.
 * Null for vendor lines, which use their repository's official address.
 */
export function sourceSlotId(software: string, line: string, method: InstallMethod | '' | undefined): string | null {
  if (method === 'standalone') return runtimeSlotId(software, line)
  if (method === 'system') return SYSTEM_MAIN_SLOT
  return null
}

/**
 * A candidate of a selectable slot that cannot be chosen: one that does not
 * serve exactly the official files. The system source in use always can:
 * choosing it changes nothing.
 */
export function candidateDisabled(slot: SourceSlot, candidate: SourceCandidate): boolean {
  return !candidate.eligible && !(slot.kind === 'system' && candidate.inUse)
}

/**
 * The candidates chosen before the user picks any: the recommended one of
 * every selectable slot, or for the system source the one in use when
 * nothing is recommended.
 */
export function defaultSelection(slots: readonly SourceSlot[]): Record<string, string> {
  const selection: Record<string, string> = {}
  for (const slot of slots) {
    if (!slot.selectable) continue
    const id = slot.selected ?? (slot.kind === 'system' ? slot.candidates.find((c) => c.inUse)?.id : undefined)
    if (id) selection[slot.id] = id
  }
  return selection
}

/**
 * The `source` to send when installing a line: the chosen candidate of its
 * slot. Undefined when the probe offered no choice for it, when nothing is
 * chosen, and when the chosen system source is the one in use (sending
 * none keeps the system's own configuration, which is the same).
 */
export function chosenSource(
  result: ProbeResult | null,
  selection: Readonly<Record<string, string>>,
  software: string,
  line: string,
  method: InstallMethod | '' | undefined,
): string | undefined {
  const slotId = sourceSlotId(software, line, method)
  const slot = slotId ? result?.slots.find((s) => s.id === slotId) : undefined
  if (!slot?.selectable) return undefined
  const candidate = slot.candidates.find((c) => c.id === selection[slot.id])
  if (!candidate || (slot.kind === 'system' && candidate.inUse)) return undefined
  return candidate.id
}
