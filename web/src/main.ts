import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { router } from './router'
import './style.css'

const app = createApp(App)
app.use(createPinia())
app.use(router)

// PWA Service Worker：注册失败要记日志（F-N3：旧代码静默 catch 掉了注册错误）。
if ('serviceWorker' in navigator && import.meta.env.PROD) {
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js').catch((err) => {
      console.warn('[WakeHub] Service Worker 注册失败（不影响使用）:', err)
    })
  })
}

app.mount('#app')
