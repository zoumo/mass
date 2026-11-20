import { defineConfig } from 'vite'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [tailwindcss()],
  base: '/observer/',
  build: { outDir: '../pkg/core/ui', emptyOutDir: true },
})
