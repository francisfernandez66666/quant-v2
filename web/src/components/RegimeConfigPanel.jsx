// RegimeConfigPanel.jsx — §N6-RISK（2026-10-21）信号生成层的风控与仓位开关。
//
// 这一组开关决定「宏观 / 大盘 / 风险情绪」如何进入信号生成：
//   · enhance.market_state_enabled —— 市场状态机是否参与风险档与仓位（大盘走向维度）；
//   · sizing.enabled / sell_boost_level —— 买入动态仓位缩放、卖出加权档位（L2=确认窗减半+取消延持）；
//   · macro_gate.* —— Yellow/Red 置信度门槛、Red 禁新开仓（应急阀）、情绪参与、N 形/动量硬拦；
//     注意 block_n_shape / block_momentum 是**一把闸管两处**：applyRiskTier（Red 风险档）与
//     applyMacroGate（期货交割日事件）读同一对键，本面板是它们唯一的页面入口，文案必须把两处
//     都点名——只写「Red 拦截」会让管理员以为关掉只影响风险档（§N6-RISK 复核时锤出，属既有
//     耦合、非本批引入：Generalize 自 §0925EVE 的 applyMacroGate 时就共用了键）。
//   · sizing.tier_scales / breadth_missing_scale —— 风险档金额权重与广度缺失降权。
//
// 纪律与战法/D1 面板一致：读取失败=禁止保存（红条 + 重载）；数字缺失不折叠成 0；
// 保存走稀疏 merge（只提交本面板负责的键）。执行层不读这些开关——它们只影响信号长什么样。
//
// English: admin panel for the signal-generation risk switches. Load failure disables saving;
// numbers are never silently folded to 0; writes are sparse merges of the keys owned here.
import React, { useCallback, useEffect, useState } from 'react'
import { Button, Card, InputNumber, Tag } from 'tdesign-react'
import ToggleSw from './ToggleSw'
import * as api from '../api/index.js'
import { showToast } from '../ui.jsx'

const rowStyle = { display: 'flex', alignItems: 'center', gap: 12, padding: '7px 0', flexWrap: 'wrap' }
const labelStyle = { width: 210, flexShrink: 0, color: 'var(--app-muted-2)', fontSize: 13 }
const hintStyle = { color: 'var(--app-muted-2)', fontSize: 12 }

function num(v, fallback) {
  const n = Number(v)
  return Number.isFinite(n) && n > 0 ? n : fallback
}

/** 风险与仓位设置面板（仅管理员挂载，与 /api/config/regime 的 adminMiddleware 同门槛）。 */
export default function RegimeConfigPanel() {
  const [state, setState] = useState('loading') // loading / loaded / error
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)
  const [baseline, setBaseline] = useState('')
  const [form, setForm] = useState(null)

  const set = useCallback((patch) => setForm((f) => ({ ...f, ...patch })), [])

  async function load() {
    setState('loading')
    setError('')
    try {
      const cfg = await api.fetchRegimeConfig()
      if (!cfg || typeof cfg !== 'object') throw new Error('返回载荷为空/非对象')
      const enh = cfg.enhance || {}
      const sz = cfg.sizing || {}
      const mg = cfg.macro_gate || {}
      const tiers = sz.tier_scales || {}
      const next = {
        marketState: !!enh.market_state_enabled,
        sizingOn: !!sz.enabled,
        sellBoost: String(sz.sell_boost_level || 'L2').toUpperCase(),
        yellowConf: num(mg.yellow_confidence, 0.90),
        redConf: num(mg.red_confidence, 0.92),
        yellowWeight: num(tiers.Yellow, 0.60),
        redWeight: num(tiers.Red, 0.30),
        redBlockNewBuy: mg.red_block_new_buy === true,
        breadthMissing: num(sz.breadth_missing_scale, 0.90),
        emotionOn: mg.emotion_enabled !== false,
        blockN: mg.block_n_shape !== false,
        blockMomentum: mg.block_momentum !== false,
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

  useEffect(() => { load() }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const dirty = form && baseline && JSON.stringify(form) !== baseline

  async function save() {
    if (state !== 'loaded' || !form) return
    // 数字字段缺失/非正拒绝提交（缺失≠0）：阈值与权重为 0 会让门槛失效或仓位归零。
    const bad = []
    if (!(form.yellowConf > 0 && form.yellowConf <= 1)) bad.push('Yellow 置信度')
    if (!(form.redConf > 0 && form.redConf <= 1)) bad.push('Red 置信度')
    if (!(form.yellowWeight > 0 && form.yellowWeight <= 1)) bad.push('Yellow 金额权重')
    if (!(form.redWeight > 0 && form.redWeight <= 1)) bad.push('Red 金额权重')
    if (!(form.breadthMissing > 0 && form.breadthMissing <= 1)) bad.push('广度缺失降权')
    if (bad.length) {
      showToast(`以下参数无效（需在 0~1 之间），禁止保存：${bad.join('、')}`, 'error')
      return
    }
    setSaving(true)
    try {
      const body = {
        enhance: { market_state_enabled: !!form.marketState },
        sizing: {
          enabled: !!form.sizingOn,
          sell_boost_level: form.sellBoost,
          breadth_missing_scale: Number(form.breadthMissing),
          tier_scales: { Yellow: Number(form.yellowWeight), Red: Number(form.redWeight) },
        },
        macro_gate: {
          yellow_confidence: Number(form.yellowConf),
          red_confidence: Number(form.redConf),
          red_block_new_buy: !!form.redBlockNewBuy,
          emotion_enabled: !!form.emotionOn,
          block_n_shape: !!form.blockN,
          block_momentum: !!form.blockMomentum,
        },
      }
      await api.setRegimeConfig(body)
      setBaseline(JSON.stringify(form))
      showToast('风控与仓位配置已保存，热更新即时生效', 'success')
    } catch (e) {
      showToast('保存失败: ' + String((e && e.message) || e), 'error')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Card title="风控与仓位（信号生成层）" style={{ marginBottom: 16 }}>
      {state === 'error' && (
        <div
          role="alert"
          style={{
            margin: '0 0 10px', padding: '8px 10px', borderRadius: 4, fontSize: 12,
            background: 'var(--app-danger-bg, #fdecee)', border: '1px solid var(--app-border, #e0e0e0)',
          }}
        >
          <div style={{ fontWeight: 600 }}>⛔ 读取失败，禁止保存</div>
          <div style={{ marginTop: 4 }}>{error || '风控配置读取失败（原因未知），已禁止保存以免覆盖他人配置。'}</div>
        </div>
      )}
      <div style={{ ...rowStyle, gap: 8 }}>
        <span className="muted" style={{ fontSize: 12 }}>
          这些开关只影响**信号怎么生成**（买入降权 / 卖出加权）；执行层只执行最终动作。
          {state === 'loading' && ' 读取中…'}
        </span>
        {state === 'error' && <Button variant="outline" size="small" onClick={load}>重载</Button>}
        {dirty && <span style={{ color: 'var(--app-warn-text)', fontSize: 12 }}>● 有未保存修改</span>}
      </div>

      {form && (
        <>
          <div style={rowStyle}>
            <span style={labelStyle}>市场状态机（大盘走向）</span>
            <ToggleSw checked={form.marketState} onChange={(v) => set({ marketState: v })} />
            <span style={hintStyle}>开启后 bull/range/bear 参与风险档与仓位；广度缺失按下方系数降权</span>
          </div>
          <div style={rowStyle}>
            <span style={labelStyle}>动态仓位（买入缩放）</span>
            <ToggleSw checked={form.sizingOn} onChange={(v) => set({ sizingOn: v })} />
            <span style={hintStyle}>按市场状态、风险档、宏观窗口、波动率等合成买入仓位倍数</span>
          </div>
          <div style={rowStyle}>
            <span style={labelStyle}>卖出加权档位</span>
            <select
              value={form.sellBoost}
              onChange={(e) => set({ sellBoost: e.target.value })}
              style={{ minWidth: 200, padding: '6px 8px', borderRadius: 4, border: '1px solid var(--app-border, #ddd)', background: 'var(--app-bg, #fff)' }}
            >
              <option value="OFF">off（不启用）</option>
              <option value="L1">L1 只标注</option>
              <option value="L2">L2 加快确认（推荐）</option>
              <option value="L3">L3 trim→close（激进）</option>
            </select>
            <Tag theme="warning" variant="light">L2：确认窗减半 + 取消延持 + Bear 单源</Tag>
          </div>
          <div style={rowStyle}>
            <span style={labelStyle}>Yellow 置信度门槛</span>
            <InputNumber min={0.01} max={1} step={0.01} value={form.yellowConf} onChange={(v) => set({ yellowConf: Number(v) })} />
            <span style={hintStyle}>非 N 形/非龙头买入低于此值降级 watch（默认 0.90）</span>
          </div>
          <div style={rowStyle}>
            <span style={labelStyle}>Red 置信度门槛</span>
            <InputNumber min={0.01} max={1} step={0.01} value={form.redConf} onChange={(v) => set({ redConf: Number(v) })} />
            <span style={hintStyle}>默认 0.92；龙头豁免该门槛（由金额权重收束）</span>
          </div>
          <div style={rowStyle}>
            <span style={labelStyle}>Yellow 金额权重</span>
            <InputNumber min={0.01} max={1} step={0.05} value={form.yellowWeight} onChange={(v) => set({ yellowWeight: Number(v) })} />
            <span style={hintStyle}>风险档 Yellow 的买入仓位乘子（默认 0.60）</span>
          </div>
          <div style={rowStyle}>
            <span style={labelStyle}>Red 金额权重</span>
            <InputNumber min={0.01} max={1} step={0.05} value={form.redWeight} onChange={(v) => set({ redWeight: Number(v) })} />
            <span style={hintStyle}>默认 0.30（收束而非一刀切）</span>
          </div>
          <div style={rowStyle}>
            <span style={labelStyle}>Red 禁止新开仓（应急）</span>
            <ToggleSw checked={form.redBlockNewBuy} onChange={(v) => set({ redBlockNewBuy: v })} />
            <span style={hintStyle}>默认关；打开后 Red 日全部买入在信号层降级 watch（含龙头）</span>
          </div>
          <div style={rowStyle}>
            <span style={labelStyle}>广度缺失降权</span>
            <InputNumber min={0.01} max={1} step={0.05} value={form.breadthMissing} onChange={(v) => set({ breadthMissing: Number(v) })} />
            <span style={hintStyle}>涨跌家数缺失时的仓位乘子（默认 0.90），数据恢复自动回真实广度</span>
          </div>
          <div style={rowStyle}>
            <span style={labelStyle}>情绪相位参与风险档</span>
            <ToggleSw checked={form.emotionOn} onChange={(v) => set({ emotionOn: v })} />
          </div>
          <div style={rowStyle}>
            <span style={labelStyle}>拦截 N 形超短（Red 档 + 交割日）</span>
            <ToggleSw checked={form.blockN} onChange={(v) => set({ blockN: v })} />
            <span style={hintStyle}>默认开。同一开关管两件事：Red 风险档日 N 形买入一律降级 watch，以及宏观事件（期货交割日）拦截 N 形超短——关掉两边一起关</span>
          </div>
          <div style={rowStyle}>
            <span style={labelStyle}>拦截动量观察（Red 档 + 交割日）</span>
            <ToggleSw checked={form.blockMomentum} onChange={(v) => set({ blockMomentum: v })} />
            <span style={hintStyle}>默认开。同上：Red 日不再新增动量 watch，交割日事件拦截也走这一把闸</span>
          </div>
          <div style={{ ...rowStyle, gap: 8 }}>
            <Button theme="primary" onClick={save} loading={saving} disabled={state !== 'loaded'}>保存风控配置</Button>
            <Button variant="outline" onClick={load} disabled={state === 'loading' || saving}>重载</Button>
          </div>
        </>
      )}
    </Card>
  )
}
