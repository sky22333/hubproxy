import { createApp } from 'vue'
import './style.css'
import App from './App.vue'
import router from './router'

if ('scrollRestoration' in history) {
  history.scrollRestoration = 'manual'
}

createApp(App).use(router).mount('#app')
