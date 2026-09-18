// ── §A7（20260918 审计批）版本可见性 e2e ──
// 覆盖今日开发的前端可观测行为：侧边栏常驻内嵌构建指纹；/api/status 的 build_commit
// 与本地指纹不一致时顶栏出现「🔄 版本漂移」横幅（APK 未随服务器重打包的典型场景）；
// 任一侧为哨兵值（unknown/dev/空）时静默不告警（本地裸 go build / CI 未注入 ldflags 不误报）。
// English: §A7 version-visibility e2e — sidebar build fingerprint, drift banner on mismatch
// (via intercepted /api/status), and sentinel silence when either side is unknown/dev/empty.
import { test, expect } from '@playwright/test'

// 读取侧边栏「build <hash>」小字里的本地构建指纹（横幅比对基准）
async function localCommit(page) {
  const txt = await page.locator('.sidebar-footer').innerText()
  const m = txt.match(/build\s+([0-9a-zA-Z]+)/)
  return m ? m[1] : ''
}

test.describe('§A7 版本可见性', () => {
  test('A7-1 侧栏常驻构建指纹；后端真实应答下不误报', async ({ page }) => {
    await page.goto('/#/dashboard')
    await expect(page.locator('.sidebar-footer'), '侧栏渲染').toBeVisible({ timeout: 15000 })
    const commit = await localCommit(page)
    expect(commit, '侧栏含 build 指纹').toMatch(/^[0-9a-f]{7,40}$|^dev$/)
    // UAT 后端为裸 go build（build_commit 空串=哨兵）→ 漂移横幅必须静默
    await expect(page.getByText('🔄'), '哨兵指纹不参与比对').toHaveCount(0)
  })

  test('A7-2 后端指纹与本地不一致 → 漂移横幅常驻并给出重打包指引', async ({ page }) => {
    await page.goto('/#/dashboard')
    await expect(page.locator('.sidebar-footer')).toBeVisible({ timeout: 15000 })
    const local = await localCommit(page)
    const remote = local === 'dev' ? 'abcdef1' : 'deadbee' // 保证与本地不相等
    await page.route('**/api/status', async (route) => {
      const res = await route.fetch() // 复用原请求（含 Authorization 头）
      const body = await res.json()
      body.build_commit = remote
      await route.fulfill({ response: res, body: JSON.stringify(body) })
    })
    // 拦截生效后等下一次 60s 轮询不现实——直接重新触发：重载页面（storageState 保持登录态）
    await page.reload()
    await expect(page.getByText('🔄'), '版本漂移横幅出现').toBeVisible({ timeout: 15000 })
    await expect(page.getByText('🔄'), '横幅含双指纹与 build_apk.sh 指引').toContainText('build_apk.sh')
  })

  test('A7-3 两侧指纹一致 → 不告警', async ({ page }) => {
    await page.goto('/#/dashboard')
    await expect(page.locator('.sidebar-footer')).toBeVisible({ timeout: 15000 })
    const local = await localCommit(page)
    await page.route('**/api/status', async (route) => {
      const res = await route.fetch()
      const body = await res.json()
      body.build_commit = local // 与内嵌前端同指纹：正常配套部署形态
      await route.fulfill({ response: res, body: JSON.stringify(body) })
    })
    await page.reload()
    await page.waitForTimeout(3000) // 给轮询留窗口
    await expect(page.getByText('🔄'), '一致时静默').toHaveCount(0)
  })
})
