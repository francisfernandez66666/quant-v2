// ── §2.12 手动维护账（2026-11-04，外脑化 B1' 批）：持仓/可用资金的手工录入面板 ──
// 桥 11-05 断后柜台不再上报持仓与资金（AGENTS「通道前提变更」），这张面板就是"手敲进去的那本账"。
//
// 三条渲染上的硬要求（都在这里，不在页面别处）：
//   1. 每次读数都必须带「手动维护，更新于 <asof>」以及「非柜台实时读数」，且用醒目的
//      warning 身份条与桥回报卡片分开——这本账一旦长得像柜台回报，就会有人拿它当证据去下真单；
//      （文案取自 utils/manualSource.js，信号卡/推送共用，勿在本文件另抄一份）
//   2. 提交是**全量**语义：清空某只票 = 删掉那一行，而不是把数量填 0；
//      面板上把这句话写在按钮旁边，因为"填 0"是所有人的第一反应；
//   3. 只改资金时**不带 positions 键**（带了就是全量替换，空数组＝清仓）——
//      这个区分在 api.saveManualBook 里判，面板这里负责把两个动作在界面上分开。
import React, { useEffect, useMemo, useState } from 'react'
import { Button, Card, Input, MessagePlugin, Tag } from 'tdesign-react'
import * as api from '../api/index.js'
// §2.12 来源标注文案（「手动维护，更新于 <asof>」「非柜台实时读数」）全站唯一源：
// 面板与买卖信号卡/推送共用同一串，避免两处各抄一份、漂一个字后有一处长得像桥回报。
import { manualSourceMain, MANUAL_SOURCE_NONREALTIME } from '../utils/manualSource.js'

// emptyRow 新增一行的初值
function emptyRow() { return { ts_code: '', name: '', qty: '', cost_price: '' } }

// toNum 文本 → 数字；空串/非法一律 NaN（调用方据此判"没填"，绝不折成 0）
function toNum(v) {
  if (v === '' || v === null || v === undefined) return NaN
  const n = Number(String(v).replace(/[, ]/g, ''))
  return Number.isFinite(n) ? n : NaN
}

// ManualBookPanel props:
//   manual —— GET /api/positions/real 返回的 manual 块（{present, positions, account, asof, note, cash_state}）
//   onChanged —— 保存成功后回调（父页据此重读实盘账）
export default function ManualBookPanel({ manual, onChanged }) {
  const [rows, setRows] = useState([emptyRow()])
  const [cash, setCash] = useState('')
  const [total, setTotal] = useState('')
  const [asof, setAsof] = useState('')
  const [saving, setSaving] = useState(false)
  const [result, setResult] = useState(null)

  // 用后端读数回填表单：父页每次重读都会带新的 manual 进来，
  // 但**只在"这轮读到了手动账"时回填**——读不到（present=false）时不许把用户正在敲的半张表冲掉。
  useEffect(() => {
    if (!manual || !manual.present) return
    const ps = Array.isArray(manual.positions) ? manual.positions : []
    setRows(ps.length ? ps.map((p) => ({ ts_code: p.ts_code || '', name: p.name || '', qty: String(p.qty ?? ''), cost_price: p.cost_price ? String(p.cost_price) : '' })) : [emptyRow()])
    const acc = manual.account || {}
    // 三态回填（后端 cash_state 已经判过一次：none/unfilled/zero/positive，这里只跟随不重算）：
    // 没行/没填 ⇒ 输入框留空（下次提交就**不带** available_cash 键），填了 0 ⇒ 回填 "0"。
    const st = manual.cash_state || 'none'
    setCash(st === 'zero' || st === 'positive' ? String(acc.account ? acc.account.available_cash : 0) : '')
    setTotal('')
    setAsof(manual.asof || '')
  }, [manual && manual.present, manual && manual.asof])

  const filled = useMemo(() => rows.filter((r) => String(r.ts_code).trim() !== ''), [rows])

  // submit 组装 body 并发送。
  // 为什么"只改资金"和"改持仓"分成两个按钮而不是一个智能提交：
  // 全量语义下端点分不清"没填"和"填了空"，把判断藏进按钮里就等于把清仓的权力交给一次误点。
  async function submit({ withPositions, clearAll }) {
    if (clearAll) {
      // 清空持仓＝带一个**空数组**提交（全量语义下的"我手上现在没有票"）。
      // 必须过确认：这一笔会把这本账的所有行删掉，而它和"保存"共用同一个端点。
      // eslint-disable-next-line no-alert
      if (typeof window !== 'undefined' && window.confirm && !window.confirm('确认清空手动持仓清单？（可用资金不受影响）')) return
      await send({ positions: [] })
      return
    }
    if (withPositions) {
      if (!filled.length) {
        MessagePlugin.warning('持仓清单为空：要清仓请点「清空持仓」，要只改资金请用下面的资金按钮')
        return
      }
      const bad = filled.find((r) => !(r.qty || '').toString().trim())
      if (bad) {
        MessagePlugin.error(bad.ts_code + ' 没填股数：手动持仓必须显式给数量（缺数量的行既算不出市值也出不了可执行信号）')
        return
      }
    }
    const body = {}
    if (withPositions) {
      body.positions = filled.map((r) => {
        const q = toNum(r.qty)
        const c = toNum(r.cost_price)
        const item = { ts_code: String(r.ts_code).trim(), qty: q }
        if (r.name) item.name = String(r.name).trim()
        if (Number.isFinite(c) && c > 0) item.cost_price = c
        return item
      })
    } else {
      const av = toNum(cash)
      if (!Number.isFinite(av)) {
        MessagePlugin.error('可用资金没填或不是数字（"没填"与"填 0"是两回事：填 0 表示真的没有可用资金）')
        return
      }
      body.available_cash = av
      const tv = toNum(total)
      if (Number.isFinite(tv) && tv > 0) body.total_asset = tv // 只带总资产会被后端 400，所以只在填了资金时才带上
    }
    const av2 = asof.toString().trim()
    if (av2) body.asof = av2
    await send(body)
  }

  // send 唯一出口：把 body 交给 api 层（那里还有一道"至少带一项"的拦截）并处理回显。
  async function send(body) {
    setSaving(true)
    try {
      const res = await api.saveManualBook(body)
      setResult(res || null)
      MessagePlugin.success((res && res.note) || '手动账已保存')
      if (onChanged) onChanged()
    } catch (e) {
      MessagePlugin.error('保存失败：' + (e && e.message ? e.message : '请求失败'))
    } finally {
      setSaving(false)
    }
  }

  const note = (result && result.note) || (manual && manual.note) || ''
  // §2.12 来源身份条的主句：优先用 manual.asof 现场拼「手动维护，更新于 <asof>」，
  // 不靠后端 note 整句——note 是给用户读的说明，随时可能改写措辞，不能当结构字段依赖。
  // asof 缺位时退回 note（旧后端只给了拼好的整句），再缺就只留「手动维护」半句，绝不伪造时刻。
  const asofText = (manual && manual.asof) ? String(manual.asof) : ''
  const sourceMain = asofText ? manualSourceMain(asofText) : (note || manualSourceMain(''))
  return (
    // data-testid 挂在**内层 div** 上：Arco 的 Card 不透传未知属性到 DOM，
    // 挂在 Card 上时测试与自动化都抓不到这块面板（11-04 实测：面板渲染出来了，
    // getByTestId 却报缺失——一个只会红在测试里的假缺失）。
    <Card title="手动维护账（桥断后的账户输入）" headerBordered>
      <div data-testid="manual-book-panel">
      {/* 来源身份条（§2.12 硬边界，本面板存在的第一理由）：
          ①「手动维护，更新于 <asof>」②「非柜台实时读数」两条必须常驻、原样上屏。
          刻意用 warning 底色 + 独立边框 + 整块 testid，与实盘桥回报卡片（默认白底表格）
          视觉上彻底分开——手敲的数一旦长得像柜台回报，就会有人拿它当证据去下真单。 */}
      <div data-testid="manual-book-source" style={{
        display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap',
        marginBottom: 10, padding: '8px 10px', borderRadius: 6,
        background: '#fffbe6', border: '1px solid #ffe58f',
      }}>
        <Tag theme="warning" variant="light-outline" data-testid="manual-book-note">{sourceMain}</Tag>
        <Tag theme="warning" variant="light-outline" data-testid="manual-book-nonrealtime">{MANUAL_SOURCE_NONREALTIME}</Tag>
        {/* 还没维护时补一句口径，别让一张空表单看起来像"已经有账" */}
        {!note && !asofText && (
          <span style={{ fontSize: 12, color: '#ad6800' }}>尚未维护手动账（当前读数仍来自柜台回报）</span>
        )}
      </div>
      <div style={{ fontSize: 12, color: '#888', marginBottom: 8 }}>
        全量清单语义：清掉某只票请**删除那一行**；把数量填 0 会被拒（0 股不是持仓，也不是清仓指令）。
      </div>
      {rows.map((r, i) => (
        <div key={i} style={{ display: 'flex', gap: 8, marginBottom: 6 }} data-testid={'manual-row-' + i}>
          <Input placeholder="代码 600000.SH" value={r.ts_code} onChange={(v) => patch(setRows, rows, i, 'ts_code', v)} style={{ width: 150 }} />
          <Input placeholder="名称（可选）" value={r.name} onChange={(v) => patch(setRows, rows, i, 'name', v)} style={{ width: 130 }} />
          <Input placeholder="股数" value={r.qty} onChange={(v) => patch(setRows, rows, i, 'qty', v)} style={{ width: 100 }} />
          <Input placeholder="成本价（可选）" value={r.cost_price} onChange={(v) => patch(setRows, rows, i, 'cost_price', v)} style={{ width: 120 }} />
          <Button size="small" variant="text" onClick={() => setRows((prev) => prev.filter((_, j) => j !== i))}>删</Button>
        </div>
      ))}
      <div style={{ display: 'flex', gap: 8, marginBottom: 10 }}>
        <Button size="small" variant="outline" onClick={() => setRows((prev) => prev.concat(emptyRow()))}>加一行</Button>
        <Button size="small" theme="primary" loading={saving} onClick={() => submit({ withPositions: true })}>保存持仓（全量）</Button>
        {/* 清仓要一次**显式**点击：全量语义下端点分不清"没填"与"填了空数组"，
            把清空挂在主按钮上等于把"一次误点抹掉全部持仓"留在路径里。 */}
        <Button size="small" variant="text" theme="danger" onClick={() => submit({ withPositions: true, clearAll: true })}>清空持仓</Button>
      </div>
      <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
        <Input placeholder="可用资金" value={cash} onChange={setCash} style={{ width: 140 }} />
        <Input placeholder="总资产（可选）" value={total} onChange={setTotal} style={{ width: 140 }} />
        <Input placeholder="asof 2026-11-04 09:30:00（留空＝当前时刻）" value={asof} onChange={setAsof} style={{ width: 260 }} />
        <Button size="small" theme="primary" loading={saving} onClick={() => submit({ withPositions: false })}>只保存资金</Button>
      </div>
      {/* 边界说明上屏：这本账不参与任何"必须新鲜"的硬闸，写了多少都不解开自动买入的 fail-close */}
      <div style={{ fontSize: 12, color: '#999', marginTop: 10 }}>
        这份读数是**用户声称**，只作为计算输入与页面读数；§M12-A 资金三态、风控闸（T+1 可卖量/集中度/买入纪律）
        与 VWAP 硬过滤都不读它，自动买入在拿不到柜台资金时照旧不动。
      </div>
      </div>
    </Card>
  )
}

// patch 改一行里的一个字段（不可变更新，避免 React 同引用不重渲染）
function patch(setRows, rows, i, key, val) {
  setRows(rows.map((r, j) => (j === i ? { ...r, [key]: val } : r)))
}
