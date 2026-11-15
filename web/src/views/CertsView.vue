<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { NH2, NTabPane, NTabs } from 'naive-ui'
import AcmeSettingsPanel from './certs/AcmeSettingsPanel.vue'
import CertList from './certs/CertList.vue'
import DnsAccounts from './certs/DnsAccounts.vue'

type Tab = 'certs' | 'dns' | 'acme'
const TABS: readonly Tab[] = ['certs', 'dns', 'acme']

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const tab = ref<Tab>(TABS.includes(route.query.tab as Tab) ? (route.query.tab as Tab) : 'certs')
const dnsRef = ref<InstanceType<typeof DnsAccounts> | null>(null)

// The tab is kept in the address so it survives a reload and can be linked.
watch(tab, (value) => {
  void router.replace({ query: value === 'certs' ? {} : { tab: value } })
})

/** From the issue dialog: open the DNS accounts tab with the new-account form. */
async function addDnsAccount() {
  tab.value = 'dns'
  await nextTick()
  dnsRef.value?.openCreate()
}
</script>

<template>
  <div class="certs">
    <NH2 class="certs__title">{{ t('certs.title') }}</NH2>
    <NTabs v-model:value="tab" type="line">
      <NTabPane name="certs" :tab="t('certs.tabs.certs')">
        <CertList @add-dns-account="addDnsAccount" />
      </NTabPane>
      <NTabPane name="dns" :tab="t('certs.tabs.dns')">
        <DnsAccounts ref="dnsRef" />
      </NTabPane>
      <NTabPane name="acme" :tab="t('certs.tabs.acme')">
        <AcmeSettingsPanel />
      </NTabPane>
    </NTabs>
  </div>
</template>

<style scoped>
.certs {
  max-inline-size: 1200px;
}

.certs__title {
  margin-block: 0 8px;
}
</style>
