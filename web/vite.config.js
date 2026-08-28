// ── Vite 构建配置 vite.config.js ──
// React 版：使用 @vitejs/plugin-react 编译 JSX，保留与 Go 后端的 /api 代理。
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// 后端实际端口：优先读环境变量，默认 8080（与 Go 后端默认端口一致）
const backendPort = process.env.VITE_BACKEND_PORT || '8080'

export default defineConfig({
  plugins: [react()],
  build: {
    // §R4-10 手动分包：把第三方库（tdesign/react/react-router 等 vendor）与应用代码拆开——
    // 业务代码改动后 vendor chunk 的内容哈希不变，用户浏览器可继续命中长缓存，
    // 首屏只需拉取小体积的应用 chunk（此前 13 页 + 全部 vendor 挤在一个 737KB 包里）。
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (!id.includes('node_modules')) return undefined
          if (id.includes('tdesign-react') || id.includes('tdesign-icons')) return 'vendor-tdesign'
          if (id.includes('react') || id.includes('scheduler') || id.includes('axios')) return 'vendor-react'
          return 'vendor-misc'
        },
      },
    },
  },
  server: {
    port: 5173,
    strictPort: false,
    proxy: {
      '/api': { target: `http://localhost:${backendPort}`, changeOrigin: true },
      '/ws': { target: `ws://localhost:${backendPort}`, ws: true },
    }
  }
})
