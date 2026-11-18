import { createApp } from 'vue'
import PrimeVue from 'primevue/config'
import Aura from '@primeuix/themes/aura'
import App from './App.vue'
import './style.css'
import 'primeicons/primeicons.css'
createApp(App).use(PrimeVue, {
 theme: { preset: Aura, options: { darkModeSelector: '.dark' } },
 csp: { nonce: document.querySelector<HTMLMetaElement>('meta[name="csp-nonce"]')?.content },
 locale: { aria: { close: '关闭', selectRow: '选中记录', unselectRow: '取消选中记录' } },
}).mount('#app')
