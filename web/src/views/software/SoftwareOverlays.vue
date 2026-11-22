<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useSessionStore } from '@/stores/session'
import { useSoftwareStore } from '@/stores/software'
import InstallSheet from './InstallSheet.vue'
import SoftwareDrawer from './SoftwareDrawer.vue'

// The software settings drawer and the install sheet, for every page that
// opens them (software, databases). The install sheet is mounted when first
// opened.
const store = useSoftwareStore()
const session = useSessionStore()

const canManage = computed(() => session.can('software.manage'))
const installUsed = ref(false)
watch(
  () => store.installOpen,
  (open) => {
    if (open) installUsed.value = true
  },
  { immediate: true },
)
</script>

<template>
  <SoftwareDrawer />
  <InstallSheet v-if="canManage && installUsed" />
</template>
