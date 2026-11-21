// SPDX-License-Identifier: AGPL-3.0-only
//
// Status colours of the interface spec (section 3.3). Every state shown with
// a colour also carries a text, so colour is never the only signal.

/** success: running, valid; warning: expiring, not set up; destructive: failed; neutral: stopped, off; progress: in progress. */
export type Tone = 'success' | 'warning' | 'destructive' | 'neutral' | 'progress'

/** Text colour of a tone (icons, labels). */
export const TONE_TEXT: Record<Tone, string> = {
  success: 'text-success',
  warning: 'text-warning',
  destructive: 'text-destructive',
  neutral: 'text-muted-foreground',
  progress: 'text-primary',
}

/** Fill of a status dot. */
export const TONE_DOT: Record<Tone, string> = {
  success: 'bg-success',
  warning: 'bg-warning',
  destructive: 'bg-destructive',
  neutral: 'bg-muted-foreground',
  progress: 'bg-primary',
}

/** Soft background with its text colour (pills, notices). */
export const TONE_SOFT: Record<Tone, string> = {
  success: 'bg-success-soft text-success',
  warning: 'bg-warning-soft text-warning',
  destructive: 'bg-destructive-soft text-destructive',
  neutral: 'bg-muted text-muted-foreground',
  progress: 'bg-primary-soft text-primary-soft-foreground',
}

/** Colour of a usage bar: warning from 70 %, destructive from 85 % (spec, section 3.5). */
export function usageTone(percent: number): Tone {
  if (percent >= 85) return 'destructive'
  if (percent >= 70) return 'warning'
  return 'progress'
}
