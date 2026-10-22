// price_source_contract_1022.test.jsx — §C7 行情来源四态渲染锁。
//
// 反证靶心：缺行情时绝不能再渲染 ¥0.00 / 0.00%；触发价/成本价必须显式注明来源；
// 真平盘（ok=true, value=0）仍要显示 0.00%。
//
// English: §C7 render locks — missing quotes never render ¥0.00 / 0.00%; trigger/cost are
// labelled; a genuine flat move (ok=true, value=0) still shows 0.00%.
import React from 'react'
import { render, screen } from '@testing-library/react'
import { describe, it, expect } from 'vitest'
import { PriceCell, PctCell } from '../components/priceFormat.jsx'

describe('§C7 PriceCell 四态', () => {
  it('live → ¥xx.xx', () => {
    render(<PriceCell value={9.99} kind="live" />)
    expect(screen.getByText('¥9.99')).toBeInTheDocument()
  })

  it('trigger → — + 触发价，不显示 ¥0.00', () => {
    const { container } = render(<PriceCell value={9.99} kind="trigger" triggerPrice={9.99} />)
    expect(screen.getByTestId('wl-price-trigger')).toBeInTheDocument()
    expect(screen.getByText(/触发价 ¥9\.99/)).toBeInTheDocument()
    expect(container.textContent).not.toContain('¥0.00')
  })

  it('cost → — + 按成本列示', () => {
    render(<PriceCell value={10} kind="cost" />)
    expect(screen.getByTestId('wl-price-cost')).toBeInTheDocument()
    expect(screen.getByText('按成本列示')).toBeInTheDocument()
  })

  it('none → —', () => {
    render(<PriceCell value={0} kind="none" />)
    expect(screen.getByTestId('wl-no-data')).toBeInTheDocument()
  })

  it('旧签名（无 kind）保持原行为：0 → —', () => {
    render(<PriceCell value={0} />)
    expect(screen.getByTestId('wl-no-data')).toBeInTheDocument()
  })
})

describe('§C7 PctCell ok 语义', () => {
  it('ok=false → —，即使数值是 0', () => {
    render(<PctCell value={0} ok={false} />)
    expect(screen.getByTestId('wl-no-data')).toBeInTheDocument()
  })

  it('ok=true 且 0 → 0.00%（真平盘）', () => {
    render(<PctCell value={0} ok />)
    expect(screen.getByText('0.00%')).toBeInTheDocument()
  })

  it('ok 缺省 → 保持旧行为，0 仍是合法平盘', () => {
    render(<PctCell value={0} />)
    expect(screen.getByText('0.00%')).toBeInTheDocument()
  })
})
