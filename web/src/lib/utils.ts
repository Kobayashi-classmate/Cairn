// SPDX-License-Identifier: AGPL-3.0-only
import { clsx, type ClassValue } from 'clsx'
import { extendTailwindMerge } from 'tailwind-merge'

// tailwind-merge has to know the type scale and shadows added in
// styles/main.css; otherwise `text-caption` would be taken for a text colour
// and dropped when combined with `text-muted-foreground`.
const twMerge = extendTailwindMerge({
  extend: {
    theme: {
      text: ['caption', 'detail', 'body', 'section', 'title', 'page-title', 'metric'],
      shadow: ['raise', 'overlay'],
    },
  },
})

/** Joins class names and lets later Tailwind utilities override earlier ones. */
export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs))
}
