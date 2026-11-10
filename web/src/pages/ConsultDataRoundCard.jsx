// ConsultDataRoundCard.jsx — §CONSULT-DATARICH（2026-11-10）咨询「补数轮」开关卡。
//
// 为什么不并进「行情通道（券商 L1）」那张卡：这一枚管的是 LLM 调用次数与咨询等待时长，
// 与行情装配/价格改写毫无关系；塞进去会让那张卡的语义变成"runtime 杂物间"，
// 也会顶掉它"六枚勾选框"的等值锁（那把锁盯的是行情面，不该被咨询面的改动带着一起改）。
//
// 三条展示取向与兄弟卡一致（都是本仓反复出事的位置）：
//   ① 勾选框初值取后端下发的**生效值**——出厂开（配置里没这个键＝开），从本地 false 起步
//      会让"一进卡就点保存"用稀疏 merge 把出厂开悄悄关掉；
//   ② 只发"当前值与已加载值不同"的键，没改动就明说没有改动，不制造一次假保存；
//   ③ 把真实代价写在卡上（调用数与 token 约翻倍、串行多等最多 8 秒），而不是只写"增强数据"——
//      开关要由代价决定，不该靠人去翻运行手册才知道它贵在哪。
import React, { useEffect, useState } from 'react'
import { Button, Card, Tag } from 'tdesign-react'
import * as api from '../api/index.js'

// ROUND_TIMEOUT_LABEL 卡面文案里的等待上限，与后端 consultDataRoundTimeout 同口径（8 秒）。
// 后端改了这个常数就要同批改这里：卡上写"最多等 15 秒"而实际 8 秒，是另一种静默失真。
const ROUND_TIMEOUT_LABEL = '8 秒'

// ConsultDataRoundCard 咨询补数轮的读写卡（config_admin 权限由设置页统一门控）。
// 读取失败时如实显示错误并给重试入口，不静默留一张空卡——空卡会被读成"开关是关的"。
export default function ConsultDataRoundCard() {
  const [cfg, setCfg] = useState(null)
  const [on, setOn] = useState(false)
  const [saving, setSaving] = useState(false)
  const [msg, setMsg] = useState(null)

  // 读取：初值一律取生效值（consult_data_round 由后端按指针位语义折成 true/false 下发）
  const load = async () => {
    try {
      const r = await api.fetchRuntimeConfig()
      setCfg(r)
      setOn(!!r.consult_data_round)
      setMsg(null)
    } catch (e) {
      setMsg({ kind: 'error', text: `读取咨询补数轮配置失败：${e?.message || e}` })
    }
  }
  useEffect(() => { load() }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // 保存：稀疏提交——没改动就不发请求（发一次空 patch 也会留下一条"配置变更"审计噪声）
  const save = async () => {
    if (!cfg) return
    if (on === !!cfg.consult_data_round) {
      setMsg({ kind: 'info', text: '没有改动可保存' })
      return
    }
    setSaving(true)
    try {
      const r = await api.updateRuntimeConfig({ consult_data_round: on })
      setCfg(r)
      // 后端把这一枚列在 hot_keys 里（引擎每次咨询现读配置快照），所以保存即生效、不需重启；
      // 仍按后端返回的 restart_required 判读，前端不自己猜哪枚要重启。
      const needRestart = (r.restart_required || []).includes('consult_data_round')
      setMsg({
        kind: 'success',
        text: `已保存：${(r.changed || []).join('、')}${needRestart ? '（需重启 quant 才生效）' : '（保存即生效）'}`,
      })
      await load()
    } catch (e) {
      setMsg({ kind: 'error', text: `保存失败（未落盘）：${e?.message || e}` })
    } finally {
      setSaving(false)
    }
  }

  // 未加载成功：只给状态与重试，不渲染一个看起来能操作的开关
  if (!cfg) {
    return <Card title="咨询补数轮（A 轮）" style={{ marginBottom: 16 }}>
      <div style={{ fontSize: 12, color: 'var(--app-text-2)' }}>
        {msg?.text || '加载中…'}
        <Button size="small" variant="text" style={{ marginLeft: 8 }} onClick={load}>重试</Button>
      </div>
    </Card>
  }

  return (
    <Card title="咨询补数轮（A 轮）" style={{ marginBottom: 16 }}>
      <div style={{ fontSize: 12, color: 'var(--app-text-2)', marginBottom: 10 }}>
        当前状态：{cfg.consult_data_round
          ? <Tag theme="success" size="small">已开启</Tag>
          : <Tag theme="default" size="small">已关闭（一轮一次调用）</Tag>}
        {/* 代价必须与开关同屏：这一枚是唯一会把咨询调用数翻倍的开关，
            只写"数据更丰富"会让人在不知情的情况下把额度用光。 */}
        <span style={{ marginLeft: 8 }}>
          开着＝主数据块拼好后多问模型一次"还缺哪些字段"，按需补取近5日逐日主力净流入与 MA20/MA60 中期均线。
        </span>
      </div>
      <div style={{ display: 'grid', gap: 10, maxWidth: 640 }}>
        <label style={{ fontSize: 13 }}>
          启用补数轮（consult_data_round）
          <input type="checkbox" checked={on} style={{ marginLeft: 8 }}
            onChange={(e) => setOn(e.target.checked)} />
          <span style={{ marginLeft: 8, color: 'var(--app-muted-2)' }}>
            出厂开；关＝回到今天"一轮一次调用"的形态（现场回滚位，不需回退发版）
          </span>
        </label>
      </div>
      {/* 四条如实说明：贵在哪、慢在哪、失败会怎样、什么时候自动不跑。
          少写任何一条，现场就会把"额度掉得快"或"回答变慢"当成故障来排。 */}
      <div style={{ marginTop: 10, fontSize: 11, color: 'var(--app-muted-2)', lineHeight: 1.7 }}>
        <div>· 代价：咨询的调用数与 token 消耗约翻倍（一轮变两轮），计入同一份咨询日预算。</div>
        <div>· 延迟：咨询接口是同步返回，补数轮串行增加最多 {ROUND_TIMEOUT_LABEL} 等待。</div>
        <div>· 失败即跳过：超时、模型答不出合法 JSON、字段取不到数，都直接用主数据块作答，不影响主回答。</div>
        <div>· 预算留位：额度只够一次调用时自动不跑补数轮，把最后一次留给主回答（不会把它挤成"额度用尽"）。</div>
        <div>· 只补两个字段：近5日逐日主力净流入、MA20/MA60 与近60日高低；北向资金/龙虎榜/盘口五档等一律取不到，模型要了也会被丢弃。</div>
      </div>
      <div style={{ marginTop: 12 }}>
        <Button theme="primary" size="small" loading={saving} onClick={save}>保存</Button>
        {msg && <span style={{ marginLeft: 10, fontSize: 12, color: msg.kind === 'error' ? 'var(--app-up)' : 'var(--app-text-2)' }}>{msg.text}</span>}
      </div>
    </Card>
  )
}