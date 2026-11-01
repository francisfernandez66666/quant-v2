// QuoteFeedConfigCard.jsx — §SUBSCRIPTION-A（2026-11-01 裁决 4/12）行情通道运行时开关卡。
//
// 这张卡的存在理由：覆盖率阈值、量纲系数、热点池上限、主源档这四个数，以前只能改 config.json
// 再重启进程——"改一个数"等于一次发布。owner 要求后台 + 前端可配，且这批新行为一律挂开关。
// 三条展示取向都是为了避免"看起来保存了其实没生效"这类我们反复出事的地方：
//   ① 每个键同时显示**原始值与生效值**（0 的含义是"没填"，实际按出厂值跑，只显一个会骗人）；
//   ② 保存后按后端返回的 restart_required 明确标"需重启才生效"的键，前端不自己猜；
//   ③ 通道现状（feed_state / 覆盖率 / 是否已达可接管条件）直接显示在卡上——
//      开主源档这个动作应当由读数决定，不该靠人翻日志。
import React, { useEffect, useState } from 'react'
import { Button, Card, InputNumber, Tag } from 'tdesign-react'
import * as api from '../api/index.js'

const STATE_LABEL = {
  ok: ['正常', 'success'],
  auth_failed: ['未认证（去确认运营账号在册凭证）', 'danger'],
  not_wired: ['未接采集器', 'default'],
}

// stateTag 把后端状态串折成"文案 + 配色"，degraded:Jx 统一按警告显示但保留原串在 title 里
// （运维要看的是"哪一条判据命中"，J1 查网关、J2 是陈旧、J3 是读数不自洽，处置动作各不相同）。
function stateTag(state) {
  const hit = STATE_LABEL[state]
  if (hit) return <Tag theme={hit[1]} size="small">{hit[0]}</Tag>
  if (state && state.startsWith('degraded:')) {
    return <Tag theme="warning" size="small" title={`命中的判据：${state}`}>券商行情降级（{state.split(':')[1]}）</Tag>
  }
  return <Tag theme="default" size="small">{state || '未知'}</Tag>
}

export default function QuoteFeedConfigCard() {
  const [cfg, setCfg] = useState(null)
  const [form, setForm] = useState({})
  const [saving, setSaving] = useState(false)
  const [msg, setMsg] = useState(null)

  // 读取：失败时如实显示错误并给重试入口，不静默留一张空卡（空卡会被读成"都是 0"）
  const load = async () => {
    try {
      const r = await api.fetchRuntimeConfig()
      setCfg(r)
      setForm({
        qmt_feed_primary: !!r.qmt_feed_primary,
        qmt_feed_coverage_min_pct: r.qmt_feed_coverage_min_pct ?? 0,
        qmt_feed_volume_to_shares: r.qmt_feed_volume_to_shares ?? 0,
        monitor_pool_hot_max: r.monitor_pool_hot_max ?? 0,
      })
      setMsg(null)
    } catch (e) {
      setMsg({ kind: 'error', text: `读取行情通道配置失败：${e?.message || e}` })
    }
  }
  useEffect(() => { load() }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // 保存：只发"当前值与已加载值不同"的键（稀疏 merge），避免把没显示的键一起覆盖
  const save = async () => {
    const patch = {}
    if (form.qmt_feed_primary !== !!cfg.qmt_feed_primary) patch.qmt_feed_primary = form.qmt_feed_primary
    if (form.quote_per_code_source !== !!cfg.quote_per_code_source) patch.quote_per_code_source = form.quote_per_code_source
    if (form.qmt_feed_coverage_min_pct !== cfg.qmt_feed_coverage_min_pct) patch.qmt_feed_coverage_min_pct = Number(form.qmt_feed_coverage_min_pct)
    if (form.qmt_feed_volume_to_shares !== cfg.qmt_feed_volume_to_shares) patch.qmt_feed_volume_to_shares = Number(form.qmt_feed_volume_to_shares)
    if (form.monitor_pool_hot_max !== cfg.monitor_pool_hot_max) patch.monitor_pool_hot_max = Number(form.monitor_pool_hot_max)
    if (!Object.keys(patch).length) { setMsg({ kind: 'info', text: '没有改动可保存' }); return }
    setSaving(true)
    try {
      const r = await api.updateRuntimeConfig(patch)
      setCfg(r)
      setMsg({ kind: 'success', text: `已保存：${(r.changed || []).join('、')}${r.restart_required?.length ? `（其中需重启生效的键：${r.restart_required.filter(k => (r.changed || []).includes(k)).join('、') || '无'}）` : ''}` })
      await load()
    } catch (e) {
      // 后端非法值是整批拒收（400 且一个键都不落），这里把原因原样回显，不做"部分成功"的假象
      setMsg({ kind: 'error', text: `保存失败（未落盘）：${e?.message || e}` })
    } finally {
      setSaving(false)
    }
  }

  if (!cfg) {
    return <Card title="行情通道（券商 L1）" style={{ marginBottom: 16 }}>
      <div style={{ fontSize: 12, color: 'var(--app-text-2)' }}>{msg?.text || '加载中…'}</div>
    </Card>
  }

  return (
    <Card title="行情通道（券商 L1）" style={{ marginBottom: 16 }}>
      <div style={{ fontSize: 12, color: 'var(--app-text-2)', marginBottom: 10 }}>
        通道状态：{stateTag(cfg.feed_state)} · 命中 {cfg.feed_hits ?? 0}/{cfg.feed_denominator ?? 0} 只
        {Number(cfg.feed_coverage_pct || 0) > 0 ? `（覆盖率 ${Number(cfg.feed_coverage_pct).toFixed(1)}%）` : ''}
        {cfg.feed_ready_to_promote ? <Tag theme="primary" size="small" style={{ marginLeft: 6 }} title="已连续达标：可以打开主源档">本可接管</Tag> : null}
      </div>
      <div style={{ display: 'grid', gap: 10, maxWidth: 560 }}>
        <label style={{ fontSize: 13 }}>
          券商价改写快照（qmt_feed_primary）
          <input type="checkbox" checked={!!form.qmt_feed_primary} style={{ marginLeft: 8 }}
            onChange={(e) => setForm({ ...form, qmt_feed_primary: e.target.checked })} />
          <span style={{ marginLeft: 8, color: 'var(--app-muted-2)' }}>
            关＝观察模式（只统计不改价）；开＝券商 L1 成为主源
          </span>
        </label>
        <label style={{ fontSize: 13 }}>
          每票来源与时刻标记（quote_per_code_source）
          <input type="checkbox" checked={!!form.quote_per_code_source} style={{ marginLeft: 8 }}
            onChange={(e) => setForm({ ...form, quote_per_code_source: e.target.checked })} />
          {/* 混源是"券商为主 + 后备链兜底"的必然结果。不开这个，整份快照只有一个源名，
              "其中几十只其实是新浪兜的"在现场看不出来——那正是我们出过事的那类谎。 */}
          <span style={{ marginLeft: 8, color: 'var(--app-muted-2)' }}>
            开＝快照逐票带 src/as_of_ms（当前 {cfg.quote_source_mixed ?? 0} 只与整份源名不同）
          </span>
        </label>
        <label style={{ fontSize: 13 }}>覆盖率阈值 %（生效 {cfg.coverage_min_pct_effective}）
          <InputNumber value={form.qmt_feed_coverage_min_pct} min={-1} max={100} style={{ width: 140, marginLeft: 8 }}
            onChange={(v) => setForm({ ...form, qmt_feed_coverage_min_pct: v })} />
        </label>
        <label style={{ fontSize: 13 }}>量纲系数 volume→股（生效 {cfg.volume_to_shares_effective}）
          <InputNumber value={form.qmt_feed_volume_to_shares} min={0} max={100} style={{ width: 140, marginLeft: 8 }}
            onChange={(v) => setForm({ ...form, qmt_feed_volume_to_shares: v })} />
          <span style={{ marginLeft: 8, color: 'var(--app-muted-2)' }}>只接受 1/100/0；错一档 VWAP 差 100 倍</span>
        </label>
        <label style={{ fontSize: 13 }}>热点池上限（券商档，生效 {cfg.monitor_pool_hot_max_effective}）
          <InputNumber value={form.monitor_pool_hot_max} min={0} max={2000} style={{ width: 140, marginLeft: 8 }}
            onChange={(v) => setForm({ ...form, monitor_pool_hot_max: v })} />
          <span style={{ marginLeft: 8, color: 'var(--app-muted-2)' }}>降级档仍是常数 60</span>
        </label>
      </div>
      <div style={{ marginTop: 12 }}>
        <Button theme="primary" size="small" loading={saving} onClick={save}>保存</Button>
        {msg && <span style={{ marginLeft: 10, fontSize: 12, color: msg.kind === 'error' ? 'var(--app-up)' : 'var(--app-text-2)' }}>{msg.text}</span>}
      </div>
      <div style={{ marginTop: 10, fontSize: 11, color: 'var(--app-muted-2)' }}>
        保存即生效的键：{(cfg.hot_keys || []).join('、')}；需重启：{(cfg.restart_required || []).join('、')}。
        总开关 qmt_feed_enabled 属重启类（决定启动时是否创建轮询），本卡不代改。
      </div>
    </Card>
  )
}
