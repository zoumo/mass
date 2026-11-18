import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
export default defineConfig({ plugins: [vue()], base: '/observer/', build: { outDir: '../pkg/core/ui', emptyOutDir: true } })
