// ── §P2-J 读取失败台账红条（单实现，多消费者）──
// 与 utils/loadLedger.js 配对：台账 hook 负责记账，本组件负责把账目显示出来。
//
// 为什么要抽成组件而不是各页各写一段 <div>：
//  1. **testid 只有一个**。门禁 §113 的「红条在位」派生锁要扫全部接了台账的页面，
//     各页自拼 data-testid（Hotspot 初版就写成 hotspot-load-fail）会让判据退化成
//     「逐页认文案」——加一页就得改一次锁，漏改的那一页永远在锁外（§BOM-REPO-DERIVE、
//     §107 派生清单同族教训）。
//  2. **文案口径只有一个**。三句话（哪几条腿没读到 / 显示的是上一轮读数 / 成功后自动销案）
//     少一句或改一句，用户就会把「上一轮读数」当「本轮读数」，这正是本条缺陷的原始形态。
//  3. 样式（危险底 + 边框 + 小字）与 Settings/D1 面板的红条同源，不再逐页调间距。
//
// English: §P2-J — the single shared banner that renders a useLoadLedger() failure ledger.
// One testid and one wording for every page, so the gate can lock "banner in place" derivationally.
import React from 'react'
import { LOAD_LEDGER_TESTID } from '../utils/loadLedger.js'

/**
 * 渲染读取失败台账红条；无失败腿时不渲染任何节点。
 * @param {{fails: Object<string,string>, page?: string}} props fails＝useLoadLedger() 的账目表；
 *        page＝页面名（只进 data-page 供排障与测试定位，不参与判据）
 * @returns {JSX.Element|null}
 */
export default function LoadFailBanner({ fails, page }) {
  // 账目表空＝本轮所有腿都读到了，红条应当整体消失（销案由 clear() 负责，这里不做超时兜底）
  const names = Object.keys(fails || {})
  // 常量 names：局部定义
  if (!names.length) return null
  return (
    <div
      role="alert"
      data-testid={LOAD_LEDGER_TESTID}
      data-page={page || ''}
      style={{
        marginBottom: 10,
        padding: '8px 10px',
        borderRadius: 4,
        fontSize: 12,
        background: 'var(--app-danger-bg, #fdecee)',
        border: '1px solid var(--app-border, #e0e0e0)',
        color: 'var(--app-text-1, #333)',
      }}
    >
      <div style={{ fontWeight: 600 }}>⚠ 本次刷新有数据没读到：{names.join('、')}</div>
      <div style={{ marginTop: 2, color: 'var(--app-text-2)' }}>
        {names.map((k) => `${k}：${fails[k]}`).join('；')}
      </div>
      <div style={{ marginTop: 2, color: 'var(--app-text-2)' }}>
        下方对应区块显示的是上一轮成功读数（不是本轮），下一轮成功后本条自动销案。
      </div>
    </div>
  )
}
