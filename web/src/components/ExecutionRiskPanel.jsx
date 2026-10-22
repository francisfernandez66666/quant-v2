// ExecutionRiskPanel.jsx — §C1/C2/C3/C5/C6/B7 执行层风控与执行质量配置。
//
// 这些参数住在 rules.qmt（账号级），与 RegimeConfigPanel 的 sizing/enhance 分开：
//   · 行业/概念敞口、ATR 风险预算、单票集中度、行情新鲜度、涨跌停闸；
//   · 单笔最低成交额、VWAP 软回踩/硬过滤；
//   · auto_sell / auto_sell_all_sources / auto_risk_caution / sell_unified_mode。
// 保存走 POST /api/config/qmt 的指针式局部合并；未出现的键保持原值。
// 概念库覆盖率单独读 /api/config/concept/status，只展示，不在这里改全局开关。
//
// English: execution-layer risk & execution-quality panel backed by rules.qmt (account scope),
// saved through the pointer-based partial merge of POST /api/config/qmt.
import React, { useCallback, useEffect, useState } from 'react'
import { Button, Card, InputNumber, Tag } from 'tdesign-react'
import ToggleSw from './ToggleSw'
import * as api from '../api/index.js'
import { showToast } from '../ui.jsx'

const rowStyle = { display: 'flex', alignItems: 'center', gap: 12, padding: '7px 0', flexWrap: 'wrap' }
const labelStyle = { width: 260, flexShrink: 0, color: 'var(--app-muted-2)', fontSize: 13 }
const hintStyle = { color: 'var(--app-muted-2)', fontSize: 12 }
const groupStyle = { margin: '16px 0 2px', fontSize: 12, fontWeight: 600, color: 'var(--app-text-2)' }

function num(v, fallback) {
  const n = Number(v)
  return Number.isFinite(n) ? n : fallback
}

// getApiFn 安全取 API 函数：测试里 api 常是部分 mock，Vitest 对未定义导出会在“取值”时抛错。
// English: safe API accessor — Vitest partial mocks throw on reading an undefined export.
function getApiFn(name) {
  try {
    const fn = api[name]
    return typeof fn === 'function' ? fn : null
  } catch {
    return null
  }
}

function Row({ label, hint, children }) {
  return (
    <div style={rowStyle}>
      <span style={labelStyle}>{label}</span>
      {children}
      {hint && <span style={hintStyle}>{hint}</span>}
    </div>
  )
}

/** 执行层风控面板（Settings 管理员区挂载；接口为当前账号 QMT 配置）。 */
export default function ExecutionRiskPanel() {
  const [state, setState] = useState('loading')
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)
  const [form, setForm] = useState(null)
  const [baseline, setBaseline] = useState('')
  const [concept, setConcept] = useState(null)
  const [conceptErr, setConceptErr] = useState('')

  const set = useCallback((patch) => setForm((f) => ({ ...f, ...patch })), [])

  async function load() {
    setState('loading')
    setError('')
    try {
      const fetchCfg = getApiFn('fetchQMTConfig')
      if (!fetchCfg) throw new Error('实盘配置接口不可用')
      const cfg = await fetchCfg()
      if (!cfg || typeof cfg !== 'object') throw new Error('返回载荷为空/非对象')
      const rg = cfg.risk_gate || {}
      const next = {
        mode: String(cfg.mode || 'manual'),
        autoSell: cfg.auto_sell !== false,
        autoSellAllSources: cfg.auto_sell_all_sources === true,
        autoRiskCaution: cfg.auto_risk_caution === true,
        sellUnifiedMode: String(cfg.sell_unified_mode || 'shadow'),
        minOrderAmount: num(cfg.min_order_amount, 5000),
        entryPullback: num(cfg.entry_pullback_pct, 0),
        entryTimeout: num(cfg.entry_timeout_sec, 0),
        maxVwapPremium: num(cfg.max_signal_vwap_premium_pct, 0),
        vwapShadow: cfg.max_signal_vwap_premium_shadow !== false,
        sectorPct: num(rg.sector_value_pct, 0),
        sectorShadow: rg.sector_value_shadow !== false,
        conceptPct: num(rg.concept_value_pct, 0),
        conceptShadow: rg.concept_value_shadow !== false,
        riskPerTrade: num(rg.risk_per_trade_pct, 0),
        riskBudgetShadow: rg.risk_budget_shadow !== false,
        limitUpBlockBuy: rg.limit_up_block_buy === true,
        limitDownBlockSell: rg.limit_down_block_sell !== false,
        dayLoss: num(rg.day_loss_limit_pct, 0),
        concentration: num(rg.single_stock_value_pct, 0),
        staleQuoteMs: num(rg.stale_quote_ms, 0),
      }
      setForm(next)
      setBaseline(JSON.stringify(next))
      setState('loaded')
    } catch (e) {
      setForm(null)
      setBaseline('')
      setError(String((e && e.message) || e))
      setState('error')
    }
  }

  async function loadConcept() {
    setConceptErr('')
    try {
      const fetchStatus = getApiFn('fetchConceptStatus')
      if (!fetchStatus) throw new Error('概念状态接口不可用')
      const res = await fetchStatus()
      setConcept(res || null)
    } catch (e) {
      setConcept(null)
      setConceptErr(String((e && e.message) || e))
    }
  }

  useEffect(() => { load() }, []) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { loadConcept() }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const dirty = form && baseline && JSON.stringify(form) !== baseline

  async function save() {
    if (state !== 'loaded' || !form) return
    const bad = []
    const pct = (name, v, allowZero = true) => {
      const n = Number(v)
      if (!Number.isFinite(n) || n < 0 || n > 100 || (!allowZero && n === 0)) bad.push(name)
    }
    pct('行业敞口上限', form.sectorPct)
    pct('概念敞口上限', form.conceptPct)
    pct('单笔风险预算', form.riskPerTrade)
    pct('日内亏损熔断', form.dayLoss)
    pct('单票集中度', form.concentration)
    pct('VWAP 硬过滤阈值', form.maxVwapPremium)
    pct('软回踩比例', form.entryPullback)
    if (!Number.isFinite(Number(form.staleQuoteMs)) || Number(form.staleQuoteMs) < 0) bad.push('行情新鲜度阈值')
    if (!Number.isFinite(Number(form.entryTimeout)) || Number(form.entryTimeout) < 0) bad.push('软回踩超时')
    if (!Number.isFinite(Number(form.minOrderAmount)) || Number(form.minOrderAmount) < 0) bad.push('最低成交额')
    if (bad.length) {
      showToast(`以下参数无效，禁止保存：${bad.join('、')}`, 'error')
      return
    }
    setSaving(true)
    try {
      const updateCfg = getApiFn('updateQMTConfig')
      if (!updateCfg) throw new Error('实盘配置接口不可用')
      await updateCfg({
        mode: form.mode,
        auto_sell: !!form.autoSell,
        auto_sell_all_sources: !!form.autoSellAllSources,
        auto_risk_caution: !!form.autoRiskCaution,
        sell_unified_mode: form.sellUnifiedMode,
        min_order_amount: Number(form.minOrderAmount),
        entry_pullback_pct: Number(form.entryPullback),
        entry_timeout_sec: Number(form.entryTimeout),
        max_signal_vwap_premium_pct: Number(form.maxVwapPremium),
        max_signal_vwap_premium_shadow: !!form.vwapShadow,
        risk_gate: {
          sector_value_pct: Number(form.sectorPct),
          sector_value_shadow: !!form.sectorShadow,
          concept_value_pct: Number(form.conceptPct),
          concept_value_shadow: !!form.conceptShadow,
          risk_per_trade_pct: Number(form.riskPerTrade),
          risk_budget_shadow: !!form.riskBudgetShadow,
          limit_up_block_buy: !!form.limitUpBlockBuy,
          limit_down_block_sell: !!form.limitDownBlockSell,
          day_loss_limit_pct: Number(form.dayLoss),
          single_stock_value_pct: Number(form.concentration),
          stale_quote_ms: Number(form.staleQuoteMs),
        },
      })
      setBaseline(JSON.stringify(form))
      showToast('执行层风控配置已保存（开关类参数按既有队列在下一交易时段生效）', 'success')
    } catch (e) {
      showToast('保存失败: ' + String((e && e.message) || e), 'error')
    } finally {
      setSaving(false)
    }
  }

  const coverage = concept ? Number(concept.coverage_pct || 0) : 0
  const failed = concept && Array.isArray(concept.failed_concepts) ? concept.failed_concepts.length : 0

  return (
    <Card title="执行层风控与执行质量" style={{ marginBottom: 16 }}>
      {state === 'error' && (
        <div role="alert" style={{ margin: '0 0 10px', padding: '8px 10px', borderRadius: 4, fontSize: 12, background: 'var(--app-danger-bg, #fdecee)', border: '1px solid var(--app-border, #e0e0e0)' }}>
          <div style={{ fontWeight: 600 }}>⛔ 读取失败，禁止保存</div>
          <div style={{ marginTop: 4 }}>{error || '实盘配置读取失败（原因未知），已禁止保存以免覆盖配置。'}</div>
        </div>
      )}
      <div style={{ ...rowStyle, gap: 8 }}>
        <span className="muted" style={{ fontSize: 12 }}>
          判断只发生在信号/指令生成层；这些是执行/风控层参数。缺失数据一律弃权。
        </span>
        {state === 'error' && <Button variant="outline" size="small" onClick={load}>重载</Button>}
        {dirty && <span style={{ color: 'var(--app-warn-text)', fontSize: 12 }}>● 有未保存修改</span>}
      </div>

      {/* 概念库覆盖率：只读展示，门槛达标后再开概念敞口。 */}
      <div style={{ ...rowStyle, gap: 8 }}>
        <span style={labelStyle}>概念库覆盖率</span>
        {conceptErr ? (
          <span style={{ color: 'var(--app-warn-text)', fontSize: 12 }}>读取失败：{conceptErr}</span>
        ) : concept ? (
          <>
            <Tag theme={coverage >= 95 ? 'success' : 'warning'} size="small">{coverage.toFixed(1)}%</Tag>
            <span style={hintStyle}>已同步 {concept.synced_concepts || 0}/{concept.total_concepts || 0}，失败 {failed}，最近成功 {concept.last_success || '—'}</span>
          </>
        ) : (
          <span style={hintStyle}>读取中…</span>
        )}
        <Button size="small" variant="outline" onClick={loadConcept}>刷新</Button>
      </div>

      {form && (
        <>
          <div style={groupStyle}>执行模式与卖出自动化（全自动目标）</div>
          <Row label="QMT 执行模式" hint="auto=信号直接报单；manual=需人工确认。切换在下一交易时段生效。">
            <select
              data-testid="exec-mode"
              value={form.mode}
              onChange={(e) => set({ mode: e.target.value })}
              style={{ minWidth: 160, padding: '6px 8px', borderRadius: 4, border: '1px solid var(--app-border, #ddd)', background: 'var(--app-bg, #fff)' }}
            >
              <option value="manual">manual（人工确认）</option>
              <option value="auto">auto（全自动）</option>
            </select>
          </Row>
          <Row label="自动卖出（止损类）" hint="默认开；关闭后止损级建议也只提醒。">
            <ToggleSw checked={form.autoSell} onChange={(v) => set({ autoSell: v })} />
          </Row>
          <Row label="所有来源卖出全自动" hint="开启后战法自带止盈/减仓也自动执行；默认关，便于回滚。">
            <ToggleSw checked={form.autoSellAllSources} onChange={(v) => set({ autoSellAllSources: v })} />
          </Row>
          <Row label="买入谨慎层（AutoRiskCaution）" hint="默认关；开启后 Red 拒开、Yellow 降额。与 sizing 同开需确认不双乘。">
            <ToggleSw checked={form.autoRiskCaution} onChange={(v) => set({ autoRiskCaution: v })} />
          </Row>
          <Row label="卖出统一裁决模式" hint="本批保持 shadow；切 on 后只有 unified 处置自动执行。">
            <select
              data-testid="sell-unified-mode"
              value={form.sellUnifiedMode}
              onChange={(e) => set({ sellUnifiedMode: e.target.value })}
              style={{ minWidth: 160, padding: '6px 8px', borderRadius: 4, border: '1px solid var(--app-border, #ddd)', background: 'var(--app-bg, #fff)' }}
            >
              <option value="shadow">shadow（只留痕）</option>
              <option value="on">on（统一裁决执行）</option>
              <option value="off">off（直连旧路）</option>
            </select>
          </Row>

          <div style={groupStyle}>行业 / 概念敞口（C1）</div>
          <Row label="行业敞口上限 %" hint="0=关；30=单行业预计市值/总资产上限 30%。未知行业弃权。">
            <InputNumber min={0} max={100} step={1} value={form.sectorPct} onChange={(v) => set({ sectorPct: num(v, 0) })} />
          </Row>
          <Row label="行业敞口影子模式" hint="默认开：命中只写 risk_gates，不拒单。">
            <ToggleSw checked={form.sectorShadow} onChange={(v) => set({ sectorShadow: v })} />
          </Row>
          <Row label="概念敞口上限 %" hint="0=关；覆盖率不足/概念未知时弃权。">
            <InputNumber min={0} max={100} step={1} value={form.conceptPct} onChange={(v) => set({ conceptPct: num(v, 0) })} />
          </Row>
          <Row label="概念敞口影子模式" hint="默认开；先跑影子统计误杀率。">
            <ToggleSw checked={form.conceptShadow} onChange={(v) => set({ conceptShadow: v })} />
          </Row>

          <div style={groupStyle}>ATR 风险预算（C3）</div>
          <Row label="单笔风险预算 % 权益" hint="0=关；0.5=单笔最大亏损约为权益 0.5%。">
            <InputNumber min={0} max={100} step={0.1} value={form.riskPerTrade} onChange={(v) => set({ riskPerTrade: num(v, 0) })} />
          </Row>
          <Row label="ATR 预算影子模式" hint="默认开：只记录原本会缩到多少，不改股数。">
            <ToggleSw checked={form.riskBudgetShadow} onChange={(v) => set({ riskBudgetShadow: v })} />
          </Row>

          <div style={groupStyle}>既有硬闸（C5）</div>
          <Row label="涨停不可追买" hint="默认关；目标上线置 true。">
            <ToggleSw checked={form.limitUpBlockBuy} onChange={(v) => set({ limitUpBlockBuy: v })} />
          </Row>
          <Row label="跌停不可追卖" hint="常开；仅显式关闭才失效。">
            <ToggleSw checked={form.limitDownBlockSell} onChange={(v) => set({ limitDownBlockSell: v })} />
          </Row>
          <Row label="日内已实现亏损熔断 %" hint="0=关；达到阈值停止当日新买入。">
            <InputNumber min={0} max={100} step={0.5} value={form.dayLoss} onChange={(v) => set({ dayLoss: num(v, 0) })} />
          </Row>
          <Row label="单票集中度上限 %" hint="0=关；买入后该票市值/总资产上限。">
            <InputNumber min={0} max={100} step={1} value={form.concentration} onChange={(v) => set({ concentration: num(v, 0) })} />
          </Row>
          <Row label="行情新鲜度阈值 ms" hint="0=关；超过该陈旧度拒单。">
            <InputNumber min={0} max={600000} step={1000} value={form.staleQuoteMs} onChange={(v) => set({ staleQuoteMs: num(v, 0) })} />
          </Row>
          <Row label="单笔最低成交额 元" hint="默认 5000；目标 10000。降档后的碎单会被拒。">
            <InputNumber min={0} max={1000000} step={1000} value={form.minOrderAmount} onChange={(v) => set({ minOrderAmount: num(v, 5000) })} />
          </Row>

          <div style={groupStyle}>VWAP 执行质量（C2，默认只落库）</div>
          <Row label="软回踩比例 %" hint="0=关；挂单价 = min(信号价, VWAP×(1−该比例))。">
            <InputNumber min={0} max={100} step={0.1} value={form.entryPullback} onChange={(v) => set({ entryPullback: num(v, 0) })} />
          </Row>
          <Row label="软回踩超时撤单 秒" hint="0=沿用既有 cancel_stale_sec 撤单闭环。">
            <InputNumber min={0} max={3600} step={10} value={form.entryTimeout} onChange={(v) => set({ entryTimeout: num(v, 0) })} />
          </Row>
          <Row label="信号价高于 VWAP 硬过滤 %" hint="0=关；先影子统计误杀率，不建议直接硬开。">
            <InputNumber min={0} max={100} step={0.1} value={form.maxVwapPremium} onChange={(v) => set({ maxVwapPremium: num(v, 0) })} />
          </Row>
          <Row label="VWAP 硬过滤影子模式" hint="默认开：命中只记录“本应放弃”，不真的跳过。">
            <ToggleSw checked={form.vwapShadow} onChange={(v) => set({ vwapShadow: v })} />
          </Row>

          <div style={{ ...rowStyle, gap: 8 }}>
            <Button theme="primary" onClick={save} loading={saving} disabled={state !== 'loaded'}>保存执行层配置</Button>
            <Button variant="outline" onClick={load} disabled={state === 'loading' || saving}>重载</Button>
          </div>
        </>
      )}
    </Card>
  )
}
