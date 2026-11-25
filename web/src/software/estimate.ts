// SPDX-License-Identifier: AGPL-3.0-only
//
// Download size and time estimates from a source probe, for the install
// dialog and the wizard. Parts that cannot be estimated are reported as
// unknown rather than guessed.
import { SYSTEM_MAIN_SLOT, type ProbeResult, type SourceSlot } from '@/api/software'

export interface DownloadEstimate {
  /** Some of the lines use the package manager. */
  hasPackages: boolean
  /** Package download in bytes; null when the package manager could not tell. */
  packageBytes: number | null
  packageCount: number
  /** Lines from repositories that are not added yet are not included. */
  partial: boolean
  /** Some lines are prebuilt runtime downloads. */
  hasRuntimes: boolean
  /** Sum of the runtime sizes that are known. */
  runtimeBytes: number
  /** At least one runtime size is unknown. */
  runtimeUnknown: boolean
  /** Sum of all known parts. */
  totalBytes: number
  /** Every part is known, so `totalBytes` is the full download. */
  complete: boolean
  /** Estimated download time; null when a size or speed is missing. */
  seconds: number | null
}

function selectedSpeedAndSize(slot: SourceSlot, selection: Record<string, string>) {
  const id = selection[slot.id] ?? slot.selected
  const candidate = slot.candidates.find((c) => c.id === id)
  return { speed: candidate?.speed ?? 0, size: candidate?.size || slot.size || 0 }
}

export function estimateDownload(
  result: ProbeResult,
  selection: Record<string, string>,
): DownloadEstimate {
  let seconds: number | null = 0
  const pkg = result.packages
  const hasPackages = pkg !== undefined
  const packageBytes = pkg && pkg.known ? pkg.bytes : null
  if (hasPackages) {
    if (packageBytes === null) {
      seconds = null
    } else if (packageBytes > 0) {
      // The package manager downloads from the chosen system source, else
      // from the one the system uses.
      const main = result.slots.find((slot) => slot.id === SYSTEM_MAIN_SLOT)
      const chosen = main?.candidates.find((c) => c.id === selection[SYSTEM_MAIN_SLOT])
      const speed = (chosen ?? main?.candidates.find((c) => c.inUse))?.speed ?? 0
      seconds = speed > 0 ? packageBytes / speed : null
    }
  }

  let runtimeBytes = 0
  let runtimeUnknown = false
  const runtimes = result.slots.filter((slot) => slot.kind === 'runtime')
  for (const slot of runtimes) {
    const { speed, size } = selectedSpeedAndSize(slot, selection)
    if (size > 0) runtimeBytes += size
    else runtimeUnknown = true
    if (seconds !== null) seconds = size > 0 && speed > 0 ? seconds + size / speed : null
  }

  return {
    hasPackages,
    packageBytes,
    packageCount: pkg?.packages ?? 0,
    partial: pkg?.partial === true,
    hasRuntimes: runtimes.length > 0,
    runtimeBytes,
    runtimeUnknown,
    totalBytes: (packageBytes ?? 0) + runtimeBytes,
    complete: (!hasPackages || packageBytes !== null) && !runtimeUnknown,
    seconds,
  }
}
