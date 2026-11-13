<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { toCanvas } from 'qrcode'

const props = defineProps<{
  /** otpauth:// URI to encode. Rendered locally; it never leaves the browser. */
  uri: string
  /** Accessible description of the image. */
  label: string
}>()

const canvas = ref<HTMLCanvasElement | null>(null)

async function render() {
  if (!canvas.value) return
  // Dark modules on white whatever the theme: authenticator apps expect it.
  await toCanvas(canvas.value, props.uri, {
    width: 192,
    margin: 2,
    errorCorrectionLevel: 'M',
    color: { dark: '#000000', light: '#ffffff' },
  })
}

onMounted(render)
watch(() => props.uri, render)
</script>

<template>
  <canvas ref="canvas" class="totp-qr" role="img" :aria-label="label" />
</template>

<style scoped>
.totp-qr {
  display: block;
  inline-size: 192px;
  block-size: 192px;
  border-radius: 4px;
}
</style>
