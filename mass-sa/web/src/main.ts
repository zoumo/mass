import { createApp } from 'vue'
import { VueQueryPlugin } from '@tanstack/vue-query'
import PrimeVue from 'primevue/config'
import { ObserverAura } from './aura'
import 'primeicons/primeicons.css'
import App from './App.vue'
import { router } from './router'
import './style.css'

const nonce = document.querySelector<HTMLMetaElement>('meta[name="observer-style-nonce"]')?.content
createApp(App)
.use(PrimeVue, { theme: { preset: ObserverAura, options: { darkModeSelector: false } }, csp: { nonce }, ripple: false })
.use(router).use(VueQueryPlugin, {
  queryClientConfig: { defaultOptions: { queries: { retry: false, refetchOnWindowFocus: true, staleTime: 2000 } } },
}).mount('#app')
