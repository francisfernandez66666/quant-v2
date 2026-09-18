// ── Vite 构建配置 vite.config.js ──
// React 版：使用 @vitejs/plugin-react 编译 JSX，保留与 Go 后端的 /api 代理。
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { execSync } from 'node:child_process'

// 后端实际端口：优先读环境变量，默认 8080（与 Go 后端默认端口一致）
const backendPort = process.env.VITE_BACKEND_PORT || '8080'

// §A7（20260918 审计批）构建期注入 git 短指纹：与部署脚本 -ldflags -X main.buildCommit 同源
// （同一 checkout 构建后端二进制与前端 dist，两值天然一致）。APK 把 dist 内嵌进 assets 后，
// 用本地指纹比对 /api/status 的 build_commit，落后即横幅告警。git 不可用时回退 'dev'，
// 'dev'/'unknown'/空一律不参与比对（见 utils.versionMismatchNotice）。
// English: §A7 — embed the git short SHA at build time (same source as the backend's ldflags
// stamp) so the APK's bundled frontend can detect drift against the deployed backend.
function buildCommit() {
  if (process.env.BUILD_COMMIT) return process.env.BUILD_COMMIT
  try {
    return execSync('git rev-parse --short HEAD', { cwd: process.cwd(), stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim() || 'dev'
  } catch (_) {
    return 'dev'
  }
}

export default defineConfig({
  plugins: [react()],
  define: {
    // 全局常量：构建期文本替换为字符串字面量，前端代码以 APP_BUILD_COMMIT 读取
    __BUILD_COMMIT__: JSON.stringify(buildCommit()),
  },
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
