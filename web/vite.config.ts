import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  server: {
    proxy: {
      // 开发代理目标可用 VITE_DEV_API_TARGET 覆盖；生产构建用相对路径 /api 同源部署（F-N8）。
      '/api': process.env.VITE_DEV_API_TARGET || 'http://127.0.0.1:8080',
    },
  },
})
