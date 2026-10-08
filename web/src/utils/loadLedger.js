// §P2-J（2026-10-06 修复批 波 6）「读取失败按腿记账」共用台账。
//
// 缺陷原文（本仓反复出现的同族，报告 AUDIT_20261005 记为 P2-J 与 P3 前端若干条）：
// 页面把多路数据各自包在 `try { ... } catch (_) {}` 里"独立容错"，容错本身是对的
// （一路挂掉不该拖黑整页），坏在**失败之后什么都不留**——界面照常显示上一轮读数或空列表，
// 没有任何一处说明"这不是最新的"。于是运维只能靠"今天怎么没有热点/持仓变空了"反推链路坏了，
// 而反推的方向经常是错的（09-29 那次「备份链明明有 8 份快照、演练却报空」就是把**读法坏了**
// 当成**数据没了**，与本条同形）。
//
// 台账的三条设计：
//  1. **按腿记名**而不是一个全局 bool：哪一路没读到就点哪一路，处置不同（板块挂了排名还可看，
//     板块+评分双挂则整页读数过期）；
//  2. **成功即销案**（clear）：失败态不写过期时间也不累计，否则昨天的抖动会一直冒充今天的风警
//     （与 §DEADGAUGE「对账干净必须归 0」同一诉求）；
//  3. 失败**不清空既有数据**：红条只报"哪条腿失败"，读数保留上一轮——把旧值抹掉只会让
//     "读取失败"看起来像"今天真的没有数据"，那是更坏的可观测性（§0929 ④「未落库不得脏缓存」同族）。
//
// English: §P2-J — shared per-leg "load failure ledger" hook. Failures are recorded by leg name,
// cleared on the next success, and never clear the last good readings; pages render this ledger as
// a red banner so a swallowed catch can no longer masquerade as fresh (or empty) data.
import { useCallback, useState } from 'react'

/**
 * 返回一份按腿记账的读取失败台账。
 * @returns {{fails: Object<string,string>, mark: (name: string, msg?: string) => void,
 *            clear: (name: string) => void, failNames: string[]}}
 */
export function useLoadLedger() {
  // 腿名 → 失败原因（成功即从表里删除，所以表非空＝"此刻仍有腿没读到"）
  const [fails, setFails] = useState({})
  // 记一条腿失败：只动自己那一格，其他腿的失败态不能被后到的成功顺带抹掉
  const mark = useCallback((name, msg) => {
    setFails((prev) => ({ ...prev, [name]: String((msg && String(msg)) || '未知错误') }))
  }, [])
  // 销一条腿的案：本轮成功即撤销红条里的对应点名
  const clear = useCallback((name) => {
    setFails((prev) => {
      if (!(name in prev)) return prev // 没记过就不触发重渲染（轮询每 30s 都会调，避免无谓 diff）
      const next = { ...prev }
      delete next[name]
      return next
    })
  }, [])
  return { fails, mark, clear, failNames: Object.keys(fails) }
}

// 台账红条的统一文案前缀与 testid 常量——页面自己拼字符串的话，
// 门禁的"红条在位"锁就得逐页认文案（每加一页改一次判据），收到这里只有一处。
// English: shared banner markers so gate locks can find the ledger by one constant, not per page.
export const LOAD_LEDGER_TESTID = 'load-ledger'
