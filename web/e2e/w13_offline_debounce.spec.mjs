// ── §0926E2E-13（FIX_PLAN_20260926E2E 四波 13 项）离线判定去抖 Playwright 锁 ──
// 真实浏览器形态的双锁之一（行为语义主锁在 vitest w4_offline_debounce.test.jsx）：
//  L-A 单次 /api/status 失败：出灰条「网络瞬时抖动」，且断联横幅「无法连接服务器」不得出现；
//  L-B 持续失败：第二轮轮询（60s 间隔）连败后才升级出断联横幅——证明去抖只是延后，不是失明。
// 依赖正规 uat_bootstrap 栈（storageState 已登录，见 playwright.config.mjs 的 setup 工程）。
// English: §0926E2E-13 Playwright lock — first failed status poll shows the gray stale strip
// (no offline banner); the amber banner only appears after the second consecutive failure.
//
// §0926E2E-13b（09-27 实跑锤出的两条现场事实，决定了本文件的判据形态）：
//  ① dev StrictMode 双挂载曾让 App 的启动轮询连发两根（已由 App.jsx 的 active 守卫修复，
//     vitest L4 锁死「一次挂载恰 1 根」）；
//  ② /api/status 的发起者不止 App 壳层——Dashboard 页自身（Dashboard.jsx:201）、
//     SSE 取票失败探测（api/index.js connectSSE）、SSE 事件触发的 refreshStatus 都会打它。
//     按"第几根请求"设卡会拨错对象（首发实为 Dashboard 的），故本文件按**时间窗**设卡，
//     并整段掐死 SSE（abort /api/events*：不签发票→无推流→无事件驱动补拉→探测仅 t≈0 一根），
//     窗口内 App 的 /api/status 只剩挂载根与 60s 间隔根，判据恢复确定性。
//  落点页选 /#/signals：该页自身零 fetchStatus 消费（全仓 grep 锤实：消费页=Dashboard/
//  Hotspot/Watchlist/Positions/Settings），不往时间窗里掺第三方请求。
import { test, expect } from '@playwright/test'

// 时间窗判据用「相对 goto 的毫秒数」而非请求计数；窗口边界取 55s——
// 早于挂载后第 2 根 60s 轮询（dev 首屏转译慢也才 ~10s 起步），晚于第 1 根与所有启动探测。
const WINDOW_START_MS = 55000

async function mountStatusWindow(page, failFromMs) {
  const t0 = Date.now()
  // 掐死 SSE：票据与推流全断（api 层探测只在 t≈0 发一根 /api/status，落哪个窗口都无害——
  // 它失败与否都不触碰 App 的连续失败计数）。
  await page.route('**/api/events**', (route) => route.abort())
  // /api/status 按时间窗放行/击落：t<failFromMs 全放行（在线基线），之后全击落。
  await page.route('**/api/status**', (route) =>
    Date.now() - t0 < failFromMs ? route.continue() : route.abort())
  await page.goto('/#/signals')
}

test('L-A 单次状态失败：灰条出现且断联横幅不出现', async ({ page }) => {
  // 基线成功（<55s）→ 第 2 根 60s 轮询击落 → 灰条；下一条轮询在 ~120s，断言窗口内恰一败。
  test.setTimeout(150000)
  await mountStatusWindow(page, WINDOW_START_MS)
  await expect(page.getByText('服务在线')).toBeVisible({ timeout: 15000 }) // 首轮成功=在线基线
  await expect(page.getByText(/网络瞬时抖动/)).toBeVisible({ timeout: 90000 }) // ~60s 单败出灰条
  // 反向锁：单轮失败不得出断联横幅（旧实现在此当场闪「无法连接服务器」）
  await expect(page.getByText(/无法连接服务器/)).not.toBeVisible()
  // 顶栏仍判在线（去抖的第一条语义：一败≠离线）
  await expect(page.getByText('服务在线')).toBeVisible()
})

test('L-B 连续两轮失败：断联横幅必须出现（去抖不失明）', async ({ page }) => {
  // 从 t=0 起全击落：挂载根第 1 败→灰条通道；60s 间隔根第 2 败→满阈值升断联横幅、灰条让位。
  test.setTimeout(150000)
  await mountStatusWindow(page, 0)
  // 第 1 败：灰条通道（离线横幅不得出现）
  await expect(page.getByText(/网络瞬时抖动/)).toBeVisible({ timeout: 15000 })
  await expect(page.getByText(/无法连接服务器/)).not.toBeVisible()
  // 第 2 败（下一根 60s 轮询）：升级断联横幅，灰条让位不叠显
  await expect(page.getByText(/无法连接服务器/)).toBeVisible({ timeout: 90000 })
  await expect(page.getByText(/网络瞬时抖动/)).not.toBeVisible()
})
