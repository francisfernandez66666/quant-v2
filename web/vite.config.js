// ── Vite 构建配置 vite.config.js ──
// React 版：使用 @vitejs/plugin-react 编译 JSX，保留与 Go 后端的 /api 代理。
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { execSync } from 'node:child_process'
import { writeFileSync } from 'node:fs'
import { resolve } from 'node:path'

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

const COMMIT = buildCommit()

// §A7-B（2026-09-20 实录补）：把指纹**同时落成一个文件** dist/BUILD_COMMIT。
//
// 为什么必须有这个文件（不是冗余）：注入进 JS 的那份指纹是**压缩混淆后**的字符串，
// 部署/验证脚本想读它只能靠"在包里扫 7~8 位十六进制字面量"这类猜测式解析——脆弱且
// 可能误命中 chunk 名之外的杂项哈希。而这个文件让"产物自称它由哪个 commit 构建"
// 变成一次确定性的读取，部署脚本才能在**上传前**拦住陈旧产物。
//
// 要拦的事故（2026-09-20 两次踩中，第二次就是本文件写下的原因）：
//   本地先 `npm run build`、**之后**才 commit → 包内嵌指纹停在上一版，
//   后端用新 SHA 构建上线 → 用户看到「前端与服务器版本不一致（本地 X / 服务器 Y）」横幅。
//   deploy_guangzhou.sh [2c] 现在会比对 dist/BUILD_COMMIT 与待部署 SHA，不等就强制重建。
//
// English: also stamp the fingerprint into dist/BUILD_COMMIT so the deploy script can verify
// artifact/backend parity *before* uploading — scanning minified JS for a hex literal is guesswork.
function buildCommitMarker() {
  return {
    name: 'build-commit-marker',
    // closeBundle：dist 已写完再落标记，保证标记与 bundle 出自同一次构建
    closeBundle() {
      try {
        writeFileSync(resolve(process.cwd(), 'dist', 'BUILD_COMMIT'), COMMIT + '\n', 'utf8')
      } catch (e) {
        this.warn('写入 dist/BUILD_COMMIT 失败（部署脚本将退回"产物指纹未知"路径）: ' + e.message)
      }
    },
  }
}

export default defineConfig({
  plugins: [react(), buildCommitMarker()],
  define: {
    // 全局常量：构建期文本替换为字符串字面量，前端代码以 APP_BUILD_COMMIT 读取
    __BUILD_COMMIT__: JSON.stringify(COMMIT),
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
