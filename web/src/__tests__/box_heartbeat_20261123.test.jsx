// box_heartbeat_20261123.test.jsx — §BOX-HEARTBEAT 前端读数的行为锁（2026-11-23）。
//
// 这块读数是给"不用 RDP 也知道 QMT 开没开"用的，所以三条必须钉死：
//  ① 读不到一律「读数未知」，绝不折成「客户端没在跑」（missing≠empty）；
//  ② 三态各说各的下一步：running 不下结论、stopped 指去查 QMTF-Client-Start、
//     unknown 说读数未知（探测失败与进程没了是两种处置）；
//  ③ 策略日志与进程读数互不拖累：日志缺失只说"无读数"，不许连进程一起沉默。
// English: locks for the box-heartbeat line; missing is unknown, never "client stopped".
import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import CarrierUpdatePanel, { boxHeartbeatText } from '../components/CarrierUpdatePanel.jsx'

afterEach(() => cleanup())

describe('boxHeartbeatText 分形', () => {
  it('known=false / 缺失 ⇒ 读数未知，且不出现"未运行"这种结论', () => {
    expect(boxHeartbeatText(null)).toContain('读数未知')
    expect(boxHeartbeatText({ known: false })).toContain('读数未知')
    expect(boxHeartbeatText({ known: false })).not.toContain('未运行')
  })

  it('running / stopped / unknown 各指各的处置方向', () => {
    const run = boxHeartbeatText({ known: true, client_proc: 'running', age_sec: 30 })
    expect(run).toContain('运行中')
    expect(run).toContain('心跳 30 秒前')
    const stop = boxHeartbeatText({ known: true, client_proc: 'stopped', age_sec: 30 })
    expect(stop).toContain('未运行')
    expect(stop).toContain('QMTF-Client-Start')
    const unk = boxHeartbeatText({ known: true, client_proc: 'unknown', age_sec: 30 })
    expect(unk).toContain('读数未知')
    expect(new Set([run, stop, unk]).size).toBe(3)
  })

  it('策略日志缺失只说无读数，不拖累进程读数', () => {
    const text = boxHeartbeatText({ known: true, client_proc: 'running', age_sec: 30 })
    expect(text).toContain('策略日志 无读数')
    expect(text).toContain('客户端 运行中')
  })

  it('日志年龄按秒/分钟分档', () => {
    expect(boxHeartbeatText({ known: true, client_proc: 'running', strategy_log_age_sec: 45 }))
      .toContain('45 秒前')
    expect(boxHeartbeatText({ known: true, client_proc: 'running', strategy_log_age_sec: 600 }))
      .toContain('10 分钟前')
  })
})

describe('CarrierUpdatePanel 心跳行渲染', () => {
  it('有心跳读数时上屏', () => {
    render(<CarrierUpdatePanel carrier={null} heartbeat={{
      known: true, client_proc: 'running', age_sec: 12, strategy_log_age_sec: 20,
    }} />)
    const line = screen.getByTestId('box-heartbeat')
    expect(line.textContent).toContain('客户端 运行中')
    expect(line.textContent).toContain('策略日志 20 秒前')
    expect(line.textContent).toContain('心跳 12 秒前')
  })

  it('没有心跳读数时显示读数未知', () => {
    render(<CarrierUpdatePanel carrier={null} />)
    expect(screen.getByTestId('box-heartbeat').textContent).toContain('读数未知')
  })
})