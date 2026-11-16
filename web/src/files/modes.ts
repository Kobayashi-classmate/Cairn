// SPDX-License-Identifier: AGPL-3.0-only
//
// Permission bits as the server sends them: the nine rwx bits plus setgid
// (0o2000) and sticky (0o1000). Setuid is never set through the panel.
import type { FileType } from '@/api/files'

export const SETGID = 0o2000
export const STICKY = 0o1000
/** Bits a file may get. */
export const FILE_MODE_MASK = 0o777
/** Bits a directory may get. */
export const DIR_MODE_MASK = 0o3777

/** "755", or "2775" with special bits. */
export function octal(mode: number): string {
  const value = mode & 0o7777
  return value > 0o777 ? value.toString(8).padStart(4, '0') : value.toString(8).padStart(3, '0')
}

/** Parses an octal mode ("644", "0755", "2775"); null when it is not one within `mask`. */
export function parseOctal(text: string, mask: number): number | null {
  const trimmed = text.trim()
  if (!/^[0-7]{3,4}$/.test(trimmed)) return null
  const value = parseInt(trimmed, 8)
  return (value & ~mask) === 0 ? value : null
}

const TYPE_CHAR: Record<string, string> = { dir: 'd', link: 'l', other: '?', file: '-' }

/** "drwxr-xr-x" style text, with "s" and "t" for setgid and sticky. */
export function modeString(type: FileType | string, mode: number): string {
  const bits = ['r', 'w', 'x']
  let text = TYPE_CHAR[type] ?? '-'
  for (let i = 8; i >= 0; i--) {
    const on = (mode & (1 << i)) !== 0
    text += on ? (bits[(8 - i) % 3] ?? '-') : '-'
  }
  const chars = text.split('')
  if (mode & SETGID) chars[6] = chars[6] === 'x' ? 's' : 'S'
  if (mode & STICKY) chars[9] = chars[9] === 'x' ? 't' : 'T'
  return chars.join('')
}
