// ── §F6 Playwright 冒烟测试配置 ──
// 复用已运行的前端（vite dev 或生产 preview 或广州部署实例），不做 webServer 自启，避免本地/CI 端口冲突。
// baseURL 走环境变量 E2E_BASE_URL，默认 http://localhost:5173（vite dev 端口）。仅跑 chromium，
// 关闭动画截图、失败重试 1 次、无头模式（CI）/有头（本地 PW_HEADED=1）。
// 首次需一次性下载浏览器内核：npx playwright install chromium
// English: §F6 Playwright smoke config — attaches to an already-running frontend (no self-managed
// webServer to avoid port clashes in local/CI). baseURL from E2E_BASE_URL (default vite dev 5173).
// Chromium only, headless by default (PW_HEADED=1 for headed), 1 retry. One-time `npx playwright install chromium`.
import { defineConfig, devices } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://localhost:5173'

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [['github'], ['html', { open: 'never' }]] : [['list']],
  use: {
    baseURL: BASE,
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
    headless: !process.env.PW_HEADED,
  },
  projects: [
    // setup：仅登录一次并落 storageState，规避后端 5/min 匿名登录频控（避免多用例各自登录触发 429 假失败）
    { name: 'setup', testMatch: /auth\.setup\.mjs/ },
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'], storageState: '.auth/state.json' },
      dependencies: ['setup'],
    },
  ],
})
