// SPDX-License-Identifier: MIT
// Based on shadcn-vue; see ../LICENSE.
// Variants and sizes follow the Cairn interface spec (sections 5 and 9).
import type { VariantProps } from 'class-variance-authority'
import { cva } from 'class-variance-authority'

export { default as Button } from './Button.vue'

export const buttonVariants = cva(
  "inline-flex shrink-0 items-center justify-center gap-2 rounded-md text-sm font-medium whitespace-nowrap transition-[color,background-color,border-color,box-shadow] outline-none select-none focus-visible:ring-2 focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-destructive/20 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
  {
    variants: {
      variant: {
        /** Primary: solid accent colour, at most one per page header. */
        default: 'bg-primary text-primary-foreground hover:bg-primary/90',
        /** Danger: solid destructive colour, kept apart from everyday actions. */
        destructive:
          'bg-destructive text-destructive-foreground hover:bg-destructive/90 focus-visible:ring-destructive/40',
        /** Secondary: outlined. */
        outline: 'border border-border bg-card text-foreground hover:bg-accent hover:text-accent-foreground',
        secondary: 'bg-secondary text-secondary-foreground hover:bg-secondary/80',
        ghost: 'text-foreground hover:bg-accent hover:text-accent-foreground',
        link: 'h-auto px-0 text-link underline-offset-4 hover:underline',
      },
      size: {
        default: 'h-9 px-3.5 has-[>svg]:px-3',
        sm: 'h-8 gap-1.5 rounded-sm px-3 text-detail has-[>svg]:px-2.5',
        lg: 'h-10 px-4 has-[>svg]:px-3.5',
        icon: 'size-8',
        'icon-sm': 'size-7 rounded-sm',
        'icon-lg': 'size-9',
      },
    },
    defaultVariants: {
      variant: 'default',
      size: 'default',
    },
  },
)
export type ButtonVariants = VariantProps<typeof buttonVariants>
