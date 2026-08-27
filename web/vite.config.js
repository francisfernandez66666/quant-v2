// ── Vite 构建配置 vite.config.js ──
// React 版：使用 @vitejs/plugin-react 编译 JSX，保留与 Go 后端的 /api 代理。
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// 后端实际端口：优先读环境变量，默认 8080（与 Go 后端默认端口一致）
const backendPort = process.env.VITE_BACKEND_PORT || '8080'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    strictPort: false,
    proxy: {
      '/api': { target: `http://localhost:${backendPort}`, changeOrigin: true },
      '/ws': { target: `ws://localhost:${backendPort}`, ws: true },
    }
  }
})
