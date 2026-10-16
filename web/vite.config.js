// ── Vite 构建配置 vite.config.js ──
// React 版：使用 @vitejs/plugin-react 编译 JSX，保留与 Go 后端的 /api 代理。
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { execSync } from 'node:child_process'
import { writeFileSync } from 'node:fs'
import { resolve } from 'node:path'

// 后端实际端口：优先读环境变量，默认 8080（与 Go 后端默认端口一致）
const backendPort = process.env.VITE_BACKEND_PORT || '8080'

// §BUILD-SANDBOX（2026-10-16 构建实录）：沙箱执行上下文里 vite 构建唯一活不过的关卡
// 不是代码、不是依赖树，而是**文件删除被 OS 层全局拒绝**（本机 node 删除守卫 + 安全中心
// 路径规则实测：/bin/rm 与 python os.unlink 一律 EPERM，rename 同样被拦）。
// esbuild 的 Go 服务进程退出时必须删掉自己的 IPC 管道文件（$TMPDIR/esbuild-<hash>），
// 删不动即退出码非 0，esbuild JS 库把该错误塞回 transform 承诺 → vite 以 PLUGIN_ERROR
// 终结构建。实录 8 次尝试残留 8 个管道文件，错误文本「remove <tmp>/esbuild-<hash>:
// operation not permitted」出自 Go 的 os.Remove 错误格式（Node 的 EPERM 错误长成
// 「EPERM: operation not permitted, remove ...」），据此定位到删除发生在 Go 子进程里——
// JS 侧给 fs.rmSync 打补丁够不着它，无解，只能让构建完全不碰 esbuild。
// 同族判例：2026-09-20 前端重建被 node 删除守卫拦下、脚本在 [2c] 退出（§A7-C）。
// 处置：VITE_SANDBOX_BUILD=1 时整体关断 esbuild（transform 与 minify 都不走它，
// applyToEnvironment 与 resolveEsbuildTranspileOptions 双闸均认 esbuild:false），
// CSS 改 lightningcss（进程内原生绑定，不起服务、不落临时文件），JS 放弃压缩
// （terser 未安装，没有其它进程内压缩器可用；产物满足部署 [2c] 四道闸，只是体积略大）。
// 沙箱外（unlink 可用的正常开发机）npm run build 恢复 esbuild 压缩全链路，行为零变化。
const SANDBOX_BUILD = !!process.env.VITE_SANDBOX_BUILD

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
    // §BUILD-SANDBOX 逃生闸（详细根因见文件头 §BUILD-SANDBOX 注释）：
    //   emptyOutDir=false：沙箱内 rm 被全局拒绝，清空 dist 必失败；旧 chunk 残留无害
    //     （index.html 只引用新 chunk，部署 [2c] 的指纹闸另行保证产物新鲜）
    //   target=esnext + minify=false：renderChunk 的 vite:esbuild-transpile 靠
    //     resolveEsbuildTranspileOptions 早退（target 为 esnext 且不 minify 即返回
    //     null，一次 esbuild 服务都不起）；注意不能只靠顶层 esbuild:false——
    //     @vitejs/plugin-react 的 config 钩子返回的 { jsx:'automatic' } 对象会在
    //     插件配置合并时把 false 覆盖掉（mergeConfig 对 非对象=对象 走整键覆盖）
    //   cssMinify=lightningcss：CSS 压缩同样避开 esbuild 服务（vite:css-post 路径），
    //     改走进程内原生绑定，不起子进程、不落临时管道文件
    // 四项在沙箱外（VITE_SANDBOX_BUILD 未设）全部保持 vite 缺省行为，零变化。
    ...(SANDBOX_BUILD ? { emptyOutDir: false, minify: false, target: 'esnext', cssMinify: 'lightningcss' } : {}),
    // §R4-10 手动分包：把第三方库（tdesign/react/react-router 等 vendor）与应用代码拆开——
    // 业务代码改动后 vendor chunk 的内容哈希不变，用户浏览器可继续命中长缓存，
    // 首屏只需拉取小体积的应用 chunk（此前 13 页 + 全部 vendor 挤在一个 737KB 包里）。
    //
    // §R4-10-FIX（2026-10-16 构建实录）按**包族闭包**划分，不再用子串含 'react' 猜包：
    // 旧逻辑把 react-transition-group（含 "react"）判进 vendor-react，却把它的依赖
    // prop-types / dom-helpers / @babel/runtime（名字里没有 "react"）漏进 vendor-misc，
    // 而 prop-types 又反向依赖 react-is（在 vendor-react）——两桶互相 import，
    // rollup 4.6x 的新分块检查直接报 "Circular chunk: vendor-misc -> vendor-react ->
    // vendor-misc" 构建失败（node_modules 重建后依赖树刷新才暴露：此前桶间有环但旧版
    // rollup 容忍）。现在的边方向只有一个拓扑序：vendor-react ← vendor-misc ←
    // vendor-tdesign ← 应用代码（tdesign 依赖 react 与 lodash-es/dayjs 等），无环。
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (!id.includes('node_modules')) return undefined
          // 取包名（兼容 @scope/name；嵌套 node_modules 时取最内层，pnpm/npm 均正确）
          const seg = id.split('node_modules/').pop()
          const pkg = seg.startsWith('@') ? seg.split('/').slice(0, 2).join('/') : seg.split('/')[0]
          if (pkg === 'tdesign-react' || pkg === 'tdesign-icons-react') return 'vendor-tdesign'
          // react 生态连同**它自己会 import 的辅助包**整族进同桶：缺一个（如 prop-types）
          // 就会把它和依赖它的包拆进互引的两桶，重新造出循环分块。
          if ([
            'react', 'react-dom', 'scheduler', 'react-is',
            'react-transition-group', 'react-fast-compare', 'hoist-non-react-statics',
            'prop-types', 'dom-helpers', '@babel/runtime', 'loose-envify', 'axios',
          ].includes(pkg)) return 'vendor-react'
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
