// SPDX-License-Identifier: AGPL-3.0-only
import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { installStepUp } from './auth/stepUp'
import { i18n } from './i18n'
import { createAppRouter } from './router'
import './styles/base.css'

installStepUp()

const app = createApp(App)
app.use(createPinia())
app.use(i18n)
app.use(createAppRouter())
app.mount('#app')
