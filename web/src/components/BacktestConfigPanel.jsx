// ── 回测增强设置面板 BacktestConfigPanel.jsx ──
// §回测自动增强 D：/api/research/backtest-config 的读写面板（总开关 + 滑点/流动性/
// Pareto 核心参数；分档表低频调整，用 JSON 文本域透传、服务端 ValidateBacktest 兜校验）。
// 保存非法（越界/档位单调性破坏）后端 400 → 原文提示，不落库。
import React, { useState, useEffect, useCallback } from 'react'
import { Card, Button, InputNumber, Input, MessagePlugin } from 'tdesign-react'
import ToggleSw from './ToggleSw'
import * as api from '../api/index.js'

// 表单状态 → 后端 BacktestConfig（tiers 文本解析失败时回退现值）
function toConfig(f) {
  const parseTiers = (text, fallback) => {
    if (!text || !text.trim()) return fallback
    try { return JSON.parse(text) } catch { return fallback }
  }
  return {
    enabled: f.enabled,
    order_value_yuan: 0, // 0 = 注入端以模拟盘 fixed_amount 解析（保持同源）
    paper_model_slippage_bps: f.paperModelBps,
    slippage: {
      base_bps: f.slipBase,
      volume_tiers: parseTiers(f.volTiersJson, f._cfg?.slippage?.volume_tiers || null),
      size_tiers: parseTiers(f.sizeTiersJson, f._cfg?.slippage?.size_tiers || null),
      asymmetric: f.asymmetric,
      buy_extra_bps: f.buyExtra,
      auto_calibrate: f.autoCalibrate,
      calib_min_sample: f.calibMinSample,
      calib_window_days: f.calibWindowDays,
    },
    liquidity: {
      enabled: f.liqEnabled,
      limit_up_openable_extra_bps: f.limitUpExtra,
      limit_down_sealed_extra_bps: f.limitDownExtra,
      limit_down_sealed_enabled: f.limitDownSealed,
      partial_fill_enabled: f.partialFill,
      fill_rate_min: f.fillRateMin,
    },
    pareto: {
      enabled: f.paretoEnabled,
      min_win_rate: f.pWinRate,
      min_profit_factor: f.pProfitFactor,
      min_sharpe: f.pSharpe,
      min_calmar: f.pCalmar,
      max_front_points: f.pMaxPoints,
    },
    walk_forward: f._cfg?.walk_forward || { enabled: false }, // A1 轮再接表单
  }
}

// 后端 BacktestConfig → 表单状态（null/缺段回退引擎同默认值，展示口径一致）
function fromConfig(cfg) {
  const s = cfg?.slippage || {}
  const l = cfg?.liquidity || {}
  const p = cfg?.pareto || {}
  return {
    enabled: !!cfg?.enabled,
    paperModelBps: cfg?.paper_model_slippage_bps ?? 5,
    slipBase: s.base_bps ?? 3,
    volTiersJson: s.volume_tiers?.length ? JSON.stringify(s.volume_tiers) : '',
    sizeTiersJson: s.size_tiers?.length ? JSON.stringify(s.size_tiers) : '',
    asymmetric: s.asymmetric !== false,
    buyExtra: s.buy_extra_bps ?? 1,
    autoCalibrate: s.auto_calibrate !== false,
    calibMinSample: s.calib_min_sample ?? 30,
    calibWindowDays: s.calib_window_days ?? 90,
    liqEnabled: !!l.enabled,
    limitUpExtra: l.limit_up_openable_extra_bps ?? 10,
    limitDownExtra: l.limit_down_sealed_extra_bps ?? 15,
    limitDownSealed: !!l.limit_down_sealed_enabled,
    partialFill: !!l.partial_fill_enabled,
    fillRateMin: l.fill_rate_min ?? 0.3,
    paretoEnabled: !!p.enabled,
    pWinRate: p.min_win_rate ?? 30,
    pProfitFactor: p.min_profit_factor ?? 1,
    pSharpe: p.min_sharpe ?? 0.5,
    pCalmar: p.min_calmar ?? 0.5,
    pMaxPoints: p.max_front_points ?? 50,
    _cfg: cfg || null,
  }
}

// 一行「标签 + 数字输入」的紧凑布局
function Num({ label, value, onChange, min, max, step = 1, tip }) {
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 6, margin: '4px 0' }}>
      <span style={{ fontSize: 12, width: 150, color: '#606266' }} title={tip || ''}>{label}</span>
      <InputNumber value={value} onChange={onChange} min={min} max={max} step={step} size="small" style={{ width: 120 }} />
    </div>
  )
}

// 一行「标签 + 开关」
function Sw({ label, checked, onChange, tip }) {
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8, margin: '4px 0' }}>
      <span style={{ fontSize: 12, width: 150, color: '#606266' }} title={tip || ''}>{label}</span>
      <ToggleSw checked={checked} onChange={onChange} />
    </div>
  )
}

/**
 * 回测增强设置面板（自动加载，保存走 PUT，400 原文提示）。
 * @returns {JSX.Element}
 */
export default function BacktestConfigPanel() {
  const [f, setF] = useState(null)
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [loaded, setLoaded] = useState(false)

  // 表单局部字段合并更新（不可变语义，避免嵌套对象整体重置丢字段）
  const set = useCallback((patch) => setF((cur) => ({ ...cur, ...patch })), [])

  useEffect(() => {
    if (loaded) return
    let dead = false
    setLoading(true)
    api.fetchBacktestConfig()
      .then((res) => { if (!dead) { setF(fromConfig(res.config)); setLoaded(true) } })
      .catch((e) => { MessagePlugin.error('读取回测增强配置失败: ' + (e.message || e)) })
      .finally(() => { if (!dead) setLoading(false) })
    return () => { dead = true }
  }, [loaded])

  // 保存：分档表 JSON 先行本地校验，再走 PUT（服务端 ValidateBacktest 越界 400 原文提示）；
  // 成功后重读落库的规范化值（FillDefaults 补齐字段）
  async function save() {
    if (!f) return
    for (const [name, text] of [['量额分档', f.volTiersJson], ['规模分档', f.sizeTiersJson]]) {
      if (text && text.trim()) {
        try { JSON.parse(text) } catch { MessagePlugin.error(name + ' JSON 非法，请检查'); return }
      }
    }
    setSaving(true)
    try {
      await api.saveBacktestConfig(toConfig(f))
      MessagePlugin.success('回测增强配置已保存——下次寻优/回放任务生效')
      setLoaded(false) // 重读规范化后的落库值
    } catch (e) { MessagePlugin.error('保存失败: ' + (e.message || e)) } finally { setSaving(false) }
  }

  if (loading || !f) return <Card title="回测增强设置" style={{ marginBottom: 12 }}><span style={{ color: '#888', fontSize: 12 }}>加载中...</span></Card>

  return (
    <Card title="回测增强设置" style={{ marginBottom: 12 }}>
      <div style={{ fontSize: 12, color: '#888', marginBottom: 8, lineHeight: 1.5 }}>
        回测可信度增强（动态滑点 / 流动性约束 / 多目标前沿）。全部关闭时行为与历史口径完全一致；
        配置在任务入队时注入，保存后<b>下一次</b>寻优/回放生效。
      </div>
      <Sw label="总开关" checked={f.enabled} onChange={(v) => set({ enabled: v })}
        tip="关闭=固定5bp滑点、无流动性门控、无Pareto输出（旧行为）" />
      {f.enabled && (
        <>
          <div style={{ fontWeight: 600, fontSize: 13, margin: '10px 0 2px' }}>滑点成本</div>
          <Sw label="模拟盘实测校准" checked={f.autoCalibrate} onChange={(v) => set({ autoCalibrate: v })}
            tip="用 paper_trades 实际成交滑点中位数替代配置值（扣掉模型内置滑点后取 min(买,卖)）" />
          <Num label="校准最少样本/方向" value={f.calibMinSample} onChange={(v) => set({ calibMinSample: v })} min={5} max={1000} />
          <Num label="校准回看天数" value={f.calibWindowDays} onChange={(v) => set({ calibWindowDays: v })} min={7} max={365} />
          <Num label="基准滑点 bp（兜底）" value={f.slipBase} onChange={(v) => set({ slipBase: v })} min={0} max={50} step={0.5} />
          <Sw label="买卖非对称" checked={f.asymmetric} onChange={(v) => set({ asymmetric: v })}
            tip="追买滑点大于止卖（涨停情绪买入冲击更高）" />
          {f.asymmetric && <Num label="买入额外 bp（兜底）" value={f.buyExtra} onChange={(v) => set({ buyExtra: v })} min={0} max={20} step={0.5} />}
          <Num label="模型内置滑点 bp" value={f.paperModelBps} onChange={(v) => set({ paperModelBps: v })} min={0} max={20} step={0.5}
            tip="模拟盘撮合已扣的固定滑点，校准求实测时同口径扣除" />
          <details style={{ margin: '4px 0' }}>
            <summary style={{ cursor: 'pointer', fontSize: 12, color: '#888' }}>高级：流动性分档表（JSON，留空=内置默认）</summary>
            <div style={{ fontSize: 11, color: '#888', margin: '4px 0' }}>volume_tiers：按日均成交额(万元)分档加 bp，max_volume_wan 升序、extra_bps 降序</div>
            <Input value={f.volTiersJson} onChange={(v) => set({ volTiersJson: v })} placeholder='[{"max_volume_wan":500,"extra_bps":20}]' style={{ fontSize: 11 }} />
            <div style={{ fontSize: 11, color: '#888', margin: '4px 0' }}>size_tiers：按名义额/日成交额占比分档加 bp，min_ratio 降序、extra_bps 降序</div>
            <Input value={f.sizeTiersJson} onChange={(v) => set({ sizeTiersJson: v })} placeholder='[{"min_ratio":0.05,"extra_bps":10}]' style={{ fontSize: 11 }} />
          </details>

          <div style={{ fontWeight: 600, fontSize: 13, margin: '12px 0 2px' }}>流动性约束</div>
          <Sw label="涨跌停门控 + 冲击成本" checked={f.liqEnabled} onChange={(v) => set({ liqEnabled: v })}
            tip="涨停封死不可买/跌停封死不可卖；打开日加冲击滑点" />
          {f.liqEnabled && (
            <>
              <Num label="涨停打开额外 bp" value={f.limitUpExtra} onChange={(v) => set({ limitUpExtra: v })} min={0} max={50} step={0.5} />
              <Sw label="跌停封死不可卖" checked={f.limitDownSealed} onChange={(v) => set({ limitDownSealed: v })}
                tip="卖出日跌停一字封死 → 顺延到之后首个可成交日" />
              {f.limitDownSealed && <Num label="跌停打开额外 bp" value={f.limitDownExtra} onChange={(v) => set({ limitDownExtra: v })} min={0} max={50} step={0.5} />}
              <Sw label="部分成交（按量帽缩量）" checked={f.partialFill} onChange={(v) => set({ partialFill: v })}
                tip="名义额超日成交额量帽时按可成交量部分成交，而非全有全无" />
              {f.partialFill && <Num label="最小成交比例" value={f.fillRateMin} onChange={(v) => set({ fillRateMin: v })} min={0} max={1} step={0.05} />}
            </>
          )}

          <div style={{ fontWeight: 600, fontSize: 13, margin: '12px 0 2px' }}>Pareto 多目标前沿</div>
          <Sw label="前沿输出 + 推荐解" checked={f.paretoEnabled} onChange={(v) => set({ paretoEnabled: v })}
            tip="寻优结果附带四维非支配前沿；冠军被硬门槛拦截时给出前沿内推荐解" />
          {f.paretoEnabled && (
            <>
              <Num label="门槛 胜率%" value={f.pWinRate} onChange={(v) => set({ pWinRate: v })} min={0} max={100} step={1} />
              <Num label="门槛 盈亏比" value={f.pProfitFactor} onChange={(v) => set({ pProfitFactor: v })} min={0} max={10} step={0.1} />
              <Num label="门槛 夏普" value={f.pSharpe} onChange={(v) => set({ pSharpe: v })} min={0} max={10} step={0.1} />
              <Num label="门槛 卡玛" value={f.pCalmar} onChange={(v) => set({ pCalmar: v })} min={0} max={10} step={0.1} />
              <Num label="前沿最大点数" value={f.pMaxPoints} onChange={(v) => set({ pMaxPoints: v })} min={5} max={500} />
            </>
          )}
        </>
      )}
      <div style={{ marginTop: 12 }}>
        <Button theme="primary" size="small" loading={saving} onClick={save}>保存回测增强配置</Button>
      </div>
    </Card>
  )
}
