// SPDX-License-Identifier: AGPL-3.0-only
//
// The subset of the `qrcode` package (browser build) used by the panel.
// Declared locally so the app's types do not pull in Node.js globals.
declare module 'qrcode' {
  export interface QRCodeRenderOptions {
    errorCorrectionLevel?: 'L' | 'M' | 'Q' | 'H'
    margin?: number
    width?: number
    color?: { dark?: string; light?: string }
  }

  export function toCanvas(
    canvas: HTMLCanvasElement,
    text: string,
    options?: QRCodeRenderOptions,
  ): Promise<void>
}
