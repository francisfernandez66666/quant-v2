// ── 前端单测配置 vitest.config.js ──
// jsdom 环境 + 全局断言，测试文件位于 src/__tests__/**；覆盖率用 v8。
// 并发超时参数（testTimeout/hookTimeout）与 setup.js 的 asyncUtilTimeout 配套，见 §FIX-6。
// English: frontend unit-test config (jsdom, globals, v8 coverage); timeouts pair with §FIX-6.
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: './src/__tests__/setup.js',
    // §FIX-6 并发超时收敛（2026-09-20）：并行跑 31 个 jsdom 文件时，默认 testTimeout=5000ms
    // 与放宽后的 asyncUtilTimeout（见 setup.js，5000ms）叠加易触发假超时。
    // 提高到 20s 给足 headroom；真实逻辑缺陷仍会失败（只是更慢暴露），不掩盖问题。
    // English: raise per-test timeout to give headroom above the raised async-util timeout.
    testTimeout: 20000,
    hookTimeout: 20000,
    include: ['src/__tests__/**/*.test.{js,jsx}'],
    coverage: {
      provider: 'v8',
      reporter: ['text', 'json', 'html'],
      include: ['src/**/*.{js,jsx}'],
      exclude: ['src/__tests__/**', 'src/main.jsx'],
    },
  },
})