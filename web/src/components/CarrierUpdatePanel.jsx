// CarrierUpdatePanel.jsx — §CARRIER-UPDATE（2026-11-04）「更新载体」读数与人工触发。
//
// 这块面板只回答一个问题：点了「更新载体」之后，那台物理机上**到底落下去没有**。
// 11-05 起 QMT 内置策略不能读文件、不能联网，载体文件结构上无法自更新，能替它干活的是
// 同机那个普通 Python 进程（qmt_gateway/carrier_updater.py）；云端这一侧只存"应该跑哪一版"
// 与盒子的回执。所以这里的三件事都是纯读数/纯请求，不含任何"云端去改别人机器"的动作。
//
// 三条不许被顺手优化掉的边界（都是本仓出过的缺陷形状）：
//  ① 读数缺失（carrier=null / known=false / request_state=unknown）一律显示「读数未知」，
//     **绝不折成「已是最新」**——§0925EVE-W3-E「读不到不装已知」、J0「零回包不算健康」；
//     文案里也**不许出现「已是最新」四个字**（哪怕前面带"不代表"）：这条读数是给探针/门禁
//     按子串判的，一句带否定前缀的"已是最新"和真绿在 grep 眼里没有区别（carrierStateText
//     的用例锁的就是这个子串）。
//  ② 点击成功只表示"置成了 pending，等盒子下一次轮询"，不表示"已装好"；
//     把 200 渲染成成功会造出「声称已做」那种静默失效；
//  ③ 期望段没填齐（desired_ready=false）时按钮禁用：那一格 pending 永远等不到答复。
//  ④ 触发按钮还受 **trading_admin 显式权限位**闸（§MULTI-ADMIN 前端同规，判据走 utils/perm
//     的单点 canStrict，不在页面里读角色缓存）：后端 POST /api/qmt/carrier/update 走
//     strictPermMiddleware(PermTradingAdmin)，quant_trade 的成员读得到读数、拨不动别人机器
//     的更新——入口按位禁用而不是隐藏，让"看得见但没权限"如实成一句可判读的话。
// English: read-only carrier manifest panel; unknown is never rendered (or even worded) as
// up-to-date, a successful click means "queued for the box", and the trigger is additionally
// gated on the explicit trading_admin permission bit (read stays open, the control does not).
import React, { useState } from 'react'
import { Button, Tag } from 'tdesign-react'
import { requestCarrierUpdate } from '../api/index.js'
import { canStrict } from '../utils/perm.js'

// stateText 把五种请求态各指向一句不同的话（折成同一句就等于让人猜下一步做什么）。
// unknown 独立成档：它是"清单读不出来"，不是"没有待处理请求"。
// 未知档的措辞刻意绕开「已是最新」四个字（文件头边界 ①：子串出现即让 grep 判读写混账）。
export function carrierStateText(state, known) {
  if (!known || !state || state === 'unknown') return '读数未知（清单缺失或未填齐，不是「已一致」）'
  switch (state) {
    case 'pending': return '待更新：已置位，等物理机下一次轮询接住'
    case 'acked': return '已回执：盒子答话了（版本是否一致看下面两行）'
    case 'failed': return '更新失败：看最近回执的原因，必要时人工清停更标记'
    case 'idle': return '空闲：没有待处理的人工请求'
    default: return '读数未知（未登记的态）'
  }
}

export default function CarrierUpdatePanel({ carrier }) {
  const [busy, setBusy] = useState(false)
  const [msg, setMsg] = useState('')
  const [failed, setFailed] = useState(false)

  const known = !!(carrier && carrier.known)
  const state = carrier && carrier.request_state ? carrier.request_state : 'unknown'
  const ready = !!(carrier && carrier.desired_ready)
  const want = (carrier && carrier.strategy_version) || ''
  const installed = carrier && carrier.installed ? carrier.installed.strategy_version : ''
  const matches = carrier ? carrier.installed_matches_desired : null
  const lastAck = carrier && carrier.last_ack ? carrier.last_ack.strategy_version : ''

  // 只有"清单读得出 + 期望段填齐 + 当前不是 pending + 显式持 trading_admin 位"才允许再点一次。
  // pending 期间重复点击只会把 requested_at 刷成新时刻，回执归因反而变模糊；
  // 权限那一闸与后端 strictPermMiddleware(PermTradingAdmin) 同源——读面开着、控制面按位关着。
  const canTrade = canStrict('trading_admin')
  const canClick = known && ready && state !== 'pending' && !busy && canTrade

  async function onClick() {
    if (!canClick) return
    setBusy(true)
    setMsg('')
    setFailed(false)
    try {
      await requestCarrierUpdate()
      // 只报"已排队"，不报"已更新"：真正落盘在物理机那一侧，见文件头边界 ②。
      setMsg('已置为待更新，等物理机下一次轮询；装机结果看最近回执')
    } catch (err) {
      setFailed(true)
      setMsg('触发失败：' + (err && err.message ? err.message : '请求未成功'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div data-testid="carrier-panel" style={{ marginTop: 8 }}>
      <span>载体更新：</span>
      <Tag size="small" theme={failed ? 'danger' : (state === 'acked' ? 'success' : 'default')}>
        {carrierStateText(state, known)}
      </Tag>
      <span data-testid="carrier-want" style={{ marginLeft: 8 }}>
        期望版 {want || '未提供'}
      </span>
      <span data-testid="carrier-installed" style={{ marginLeft: 8 }}>
        在跑 {installed || '暂无回报'}
      </span>
      {matches === false && (
        <span data-testid="carrier-mismatch" style={{ marginLeft: 8, color: '#d54941' }}>
          装的不等于跑的：等盒子回执或人工核版本
        </span>
      )}
      {!!lastAck && <span style={{ marginLeft: 8 }}>最近回执版 {lastAck}</span>}
      {/* tag="button" 是刻意的：TDesign 的 Button 在 disabled 时默认降级成 <div>——
          禁用的触发器就查无此钮（role 缺失），既骗过探针也骗过读屏用户。
          禁用态必须是**真实可发现的 disabled <button>**（用例锁的就是 getByRole('button').disabled）。 */}
      <Button tag="button" size="small" disabled={!canClick} loading={busy} onClick={onClick} style={{ marginLeft: 8 }}>
        更新载体
      </Button>
      {!!msg && (
        <div data-testid="carrier-msg" style={{ marginTop: 4, color: failed ? '#d54941' : undefined }}>{msg}</div>
      )}
    </div>
  )
}
