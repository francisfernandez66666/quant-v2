// ── 全局 D1 事件规则面板 D1ConfigPanel.jsx（§P2-H，2026-10-06 修复批 波 6）──
//
// 这条通道在后端早就存在并且已经被修好过一轮（§0929CFG-D1：稀疏 merge + 写前快照 +
// 字段级审计 + 持久化失败如实 500 + effective_source 回显），缺的**只有前端**：
// 本批开工前 `grep -rn 'config/d1' web/src` 零命中，于是
//   · D1 软加成权重/门槛、事件规则表这些**直接参与打分**的参数只能靠脚本或翻 auth.json 改；
//   · 后端专门为此加的 effective_source（运行时到底吃全局还是吃某账号覆盖）白回——没人显示；
//   · 「页面上写的值 ≠ 引擎真用的值」这道 §0929 反复出事的缝，在 D1 这条通道上至今没有出口。
// 本面板就是那条隐形通道的出口：读侧全量回显（含生效账本），写侧走稀疏 merge。
//
// 三条与同族面板（战法参数 §N-4）一致的纪律，都在这里重申一遍：
//  1. **读取失败＝禁止保存**（红条 + 按钮禁用 + 「重载」出口）。静默兜底空对象正是
//     §CFGSMASH 那次把五套阈值清零落库的起点，D1 是同一族，不因为"条数少"就豁免。
//  2. **缺失 ≠ 0**：boost_weight / boost_threshold 是数字必填，未填不给默认值提交；
//     要关闭加成请**明写** 0（后端注释也这么要求：显式传键才更新该键）。
//  3. **规则表整表提交**：rules 是数组，稀疏 merge 对数组的处理是"传了就整体替换"，
//     所以增删改都随整表一起写；这也是为什么读侧必须先成功——读失败时手里没有旧表，
//     提交就等于把别人的规则清空。
//
// English: §P2-H — the global D1 rule/soft-boost panel. The backend channel existed (with sparse
// merge, snapshot, audit and effective_source reporting) but had zero frontend callers, so these
// scoring-affecting parameters were invisible. Load failure disables saving; numeric fields must be
// filled (missing is never folded into 0); the rules list is submitted as a whole.
import React, { useState, useEffect, useCallback } from 'react'
import { Card, Button, Input, InputNumber, Tag } from 'tdesign-react'
import ToggleSw from './ToggleSw'
import * as api from '../api/index.js'
import { showToast } from '../ui.jsx'

// 面板内一行的编辑态形状：{key, direction, score, blocked}
// key 只是 React 列表稳定标识（uuid 递增即可），**不参与提交**——提交时按后端契约重建
// {direction, score, blocked} 三键，别把本地 key 混进请求体（未知键会被后端回报 ignored_keys，
// 但那是"发错了"的证据，不该常态出现）。
let rowSeq = 0

/** 把后端规则数组转成编辑态行（补本地 key，缺字段一律留空而不是 0）。 */
function toRows(rules) {
  // 后端可能回 null（从未配置过），Array.isArray 是唯一判据，不用 || [] 之外的兜底
  const list = Array.isArray(rules) ? rules : []
  return list.map((r) => ({
    key: (rowSeq += 1),
    direction: typeof r.direction === 'string' ? r.direction : '',
    score: Number.isFinite(Number(r.score)) ? Number(r.score) : null,
    blocked: !!r.blocked,
  }))
}

/** 编辑态行 → 后端契约对象（只留三个已知键）。 */
function toRules(rows) {
  return rows.map((r) => ({ direction: String(r.direction || ''), score: Number(r.score), blocked: !!r.blocked }))
}

const rowStyle = { display: 'flex', alignItems: 'center', gap: 12, padding: '6px 0' } // 设置项行布局
const labelStyle = { width: 160, flexShrink: 0, color: 'var(--app-muted-2)', fontSize: 13 } // 设置项标签样式

/**
 * 全局 D1 配置面板：GET/POST /api/config/d1 的读写出口（仅管理员可见，与战法参数同门槛）。
 * @returns {JSX.Element}
 */
export default function D1ConfigPanel() {
  // 规则行 + 两个加成参数
  const [rows, setRows] = useState([])
  const [boostWeight, setBoostWeight] = useState(null)
  const [boostThreshold, setBoostThreshold] = useState(null)
  // 加载三态：loading / loaded / error（error 禁保存，同 §N-4 战法参数口径）
  const [loadState, setLoadState] = useState('loading')
  const [loadError, setLoadError] = useState('')
  const [saving, setSaving] = useState(false)
  // 生效账本（global|account）：GET 回显读侧真值，POST 回显本次真实落点
  const [effectiveSource, setEffectiveSource] = useState('')
  // 基线（深拷贝）用于 dirty 提示，避免"没改也标脏"
  const [baseline, setBaseline] = useState(null)

  // 当前编辑态的序列化形式（与后端提交体同形，dirty 比对只用这一份）
  const snapshot = useCallback(() => JSON.stringify({
    rules: toRules(rows), boost_weight: boostWeight, boost_threshold: boostThreshold,
  }), [rows, boostWeight, boostThreshold])

  // 读取全局 D1 配置（初始化与「重载」共用同一口径，避免两处各写一遍而分叉）
  async function load() {
    setLoadState('loading')
    setLoadError('')
    try {
      const cfg = await api.fetchD1Config()
      if (!cfg || typeof cfg !== 'object') throw new Error('返回载荷为空/非对象')
      // 先把三份值取成局部变量，再一次性写 state + 基线：基线必须由**同一份**读回来的值算出，
      // 放在 setRows(...) 之后调 snapshot() 会读到旧闭包（React 批量更新尚未落地），
      // 结果是刚读完就被判成"有未保存修改"。
      const nextRows = toRows(cfg.rules)
      const nextWeight = Number.isFinite(Number(cfg.boost_weight)) ? Number(cfg.boost_weight) : null
      const nextThreshold = Number.isFinite(Number(cfg.boost_threshold)) ? Number(cfg.boost_threshold) : null
      setRows(nextRows)
      setBoostWeight(nextWeight)
      setBoostThreshold(nextThreshold)
      setBaseline(JSON.stringify({
        rules: toRules(nextRows), boost_weight: nextWeight, boost_threshold: nextThreshold,
      }))
      setEffectiveSource(typeof cfg.effective_source === 'string' ? cfg.effective_source : '')
      setLoadState('loaded')
    } catch (e) {
      // 读失败时把手里的值全部作废：留旧值会让人以为"改坏了还能点保存救回来"，
      // 而那份旧值可能已经是上一轮的，落库就成了拿过期规则覆盖新规则。
      setRows([])
      setBoostWeight(null)
      setBoostThreshold(null)
      setBaseline(null) // 基线一并作废：留着旧基线会让"读失败后新增一行"被算成 dirty 却永远存不了
      setLoadError(String((e && e.message) || e))
      setLoadState('error')
    }
  }

  useEffect(() => { load() }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // 保存：缺失数字字段拒绝提交（缺失≠0）；成功后用后端回显的真实落点与条数自证接线
  async function save() {
    if (loadState !== 'loaded') return
    const missing = []
    if (boostWeight === null || boostWeight === undefined || !Number.isFinite(Number(boostWeight))) missing.push('软加成权重')
    if (boostThreshold === null || boostThreshold === undefined || !Number.isFinite(Number(boostThreshold))) missing.push('加成门槛')
    // 规则表里每一行也必须齐：direction 空串或 score 非数字都会让 D1 打分拿到 0 分规则
    rows.forEach((r, i) => {
      if (!String(r.direction || '').trim()) missing.push(`规则 #${i + 1} 方向`)
      if (!Number.isFinite(Number(r.score))) missing.push(`规则 #${i + 1} 得分`)
    })
    if (missing.length) {
      showToast(`以下 D1 字段缺失，禁止保存（缺失≠0，请补齐或点「重载」）：${missing.slice(0, 4).join('、')}${missing.length > 4 ? ` 等 ${missing.length} 项` : ''}`, 'error')
      return
    }
    setSaving(true)
    try {
      const res = await api.setD1Config({ rules: toRules(rows), boost_weight: Number(boostWeight), boost_threshold: Number(boostThreshold) })
      const ignored = Array.isArray(res && res.ignored_keys) ? res.ignored_keys : []
      if (ignored.length) {
        // 稀疏 merge 会把未知键如实回报而不是静默吞掉：出现即说明前端契约漂了，必须当场可见
        showToast(`保存成功，但后端忽略了这些未知键（前端契约漂移，请核对）：${ignored.join('、')}`, 'warning')
      } else {
        showToast(`D1 配置已保存，热更新即时生效（规则 ${res && res.rules_count !== undefined ? res.rules_count : rows.length} 条）`, 'success')
      }
      // 落点回显：后端按读侧优先级决定这次写进了哪本账，界面必须跟着改口径而不是继续写"全局"
      if (res && typeof res.effective_source === 'string') setEffectiveSource(res.effective_source)
      setBaseline(snapshot())
    } catch (e) {
      showToast('D1 配置保存失败: ' + (e.message || '未知错误'), 'error')
    }
    setSaving(false)
  }

  // 行内编辑：只动指定行的指定字段（不可变更新，防整表重渲染丢焦点）
  function patchRow(key, field, value) {
    setRows((prev) => prev.map((r) => (r.key === key ? { ...r, [field]: value } : r)))
  }

  // 新增一行空规则（新行必须显式填写才允许保存，见 save() 的必填校验）
  function addRow() {
    setRows((prev) => [...prev, { key: (rowSeq += 1), direction: '', score: null, blocked: false }])
  }

  // 删除一行（提交时整表覆盖，所以删除是真实落库的动作，弹窗交给用户自己确认——
  // 这里不做二次确认的原因：面板顶部已写明"保存后整表替换"，且未点保存前仍可「重载」回退）
  function removeRow(key) {
    setRows((prev) => prev.filter((r) => r.key !== key))
  }

  const dirty = baseline !== null && baseline !== snapshot()

  // 面板骨架：三态提示条 + 两个加成参数 + 规则表 + 保存/重载
  return (
    <Card title="D1 事件规则与软加成" style={{ marginBottom: 16 }}>
      {/* §P2-H 读取失败红条：error 态禁用保存并给「重载」出口（同 §N-4 战法参数形态） */}
      {loadState === 'error' && (
        <div
          role="alert"
          style={{
            margin: '0 0 10px',
            padding: '8px 10px',
            borderRadius: 4,
            fontSize: 12,
            background: 'var(--app-danger-bg, #fdecee)',
            border: '1px solid var(--app-border, #e0e0e0)',
            color: 'var(--app-text-1, #333)',
          }}
        >
          <div style={{ fontWeight: 600 }}>⛔ D1 配置读取失败，禁止保存</div>
          <div style={{ marginTop: 4 }}>{loadError || '未知原因。为避免把缺失值写成 0 或把规则表清空，已禁止保存。'}</div>
        </div>
      )}

      <div style={rowStyle}>
        <span style={labelStyle}>生效账本</span>
        {/* effective_source 是 §0929CFG-D1 加的自证字段：account=运行时吃账号级覆盖，
            global=吃全局。没有这一格，"页面写的值就是引擎吃的值"只能靠翻 auth.json 取证。 */}
        <span style={{ fontSize: 13 }}>
          {loadState === 'loading' && '读取中…'}
          {loadState !== 'loading' && (effectiveSource === 'account'
            ? <Tag size="small" theme="warning">账号级覆盖（本面板保存会写入该账号覆盖账，而非全局）</Tag>
            : (effectiveSource === 'global'
              ? <Tag size="small" theme="success">全局</Tag>
              : <Tag size="small" theme="default">未回显</Tag>))}
        </span>
      </div>

      <div style={rowStyle}>
        <span style={labelStyle} title="非 N 战法总分的 D1 软加成权重；明写 0 表示关闭加成">软加成权重</span>
        <InputNumber value={boostWeight} onChange={setBoostWeight} step={0.01} theme="column" placeholder="如 0.15" disabled={loadState !== 'loaded'} />
      </div>
      <div style={rowStyle}>
        <span style={labelStyle} title="D1 分（0~40）低于该值不做加成">加成门槛</span>
        <InputNumber value={boostThreshold} onChange={setBoostThreshold} step={1} theme="column" placeholder="如 8" disabled={loadState !== 'loaded'} />
      </div>

      {/* 规则表：direction=事件方向/关键词标签，score=匹配得分，blocked=负面阻断标记 */}
      <div style={{ fontWeight: 600, margin: '10px 0 4px' }}>事件规则（{rows.length} 条）</div>
      {rows.length === 0 && (
        <div className="muted" style={{ fontSize: 12 }}>
          {loadState === 'loaded' ? '当前没有 D1 事件规则（后端返回空表）。新增后保存即整表替换。' : '规则读取中…'}
        </div>
      )}
      {rows.map((r, i) => (
        <div style={rowStyle} key={r.key}>
          <span style={{ ...labelStyle, width: 56 }}>规则 {i + 1}</span>
          <Input value={r.direction} placeholder="方向/关键词，如 利好" onChange={(v) => patchRow(r.key, 'direction', v)} style={{ width: 160 }} disabled={loadState !== 'loaded'} />
          <InputNumber value={r.score} step={1} theme="column" placeholder="得分" onChange={(v) => patchRow(r.key, 'score', v)} style={{ width: 110 }} disabled={loadState !== 'loaded'} />
          <span style={{ fontSize: 12, color: 'var(--app-muted-2)' }}>负面阻断</span>
          <ToggleSw checked={r.blocked} onChange={(v) => patchRow(r.key, 'blocked', v)} disabled={loadState !== 'loaded'} />
          <Button size="sm" variant="text" theme="danger" onClick={() => removeRow(r.key)} disabled={loadState !== 'loaded'}>删除</Button>
        </div>
      ))}

      <div style={{ ...rowStyle, gap: 8, flexWrap: 'wrap' }}>
        <Button size="sm" variant="outline" onClick={addRow} disabled={loadState !== 'loaded'}>新增规则</Button>
        <Button theme="primary" onClick={save} loading={saving} disabled={loadState !== 'loaded'}>保存 D1 配置</Button>
        {loadState !== 'loaded' && (
          <Button variant="outline" onClick={load} disabled={loadState === 'loading'}>重载</Button>
        )}
        {dirty && <span style={{ color: 'var(--app-warn-text)', fontSize: 12 }}>● 有未保存修改</span>}
      </div>
      <div style={{ fontSize: 12, color: 'var(--app-text-2)', marginTop: 6 }}>
        保存为稀疏提交（三个已知键），但 <b>rules 是整表替换</b>：增删改都随全表写入；加成要关闭请明写 0。
      </div>
    </Card>
  )
}
