// ── §F6 命令面板 CommandPalette.jsx ──
// Ctrl/Cmd+K 呼出的全局命令面板：模糊搜索页面跳转 + 六位代码时提供"查看个股详情"（复用 F3 抽屉）。
// 键盘优先（↑/↓ 选择、Enter 执行、Esc 关闭），点遮罩/条目亦可。纯过滤逻辑 filterCommands 可单测。
// English: §F6 global command palette (Ctrl/Cmd+K): fuzzy-search page navigation, and when the query
// looks like a 6-digit code, offer "open stock detail" (reuses the F3 drawer). Keyboard-first
// (↑/↓ move, Enter run, Esc close); overlay/row click also work. filterCommands is a pure, unit-tested fn.
import React, { useState, useEffect, useMemo, useRef } from 'react'
import { useNavigate } from 'react-router-dom'

// filterCommands 按查询串对命令做子序列匹配（大小写不敏感），返回带匹配序的过滤结果。空查询返回全部。
// English: subsequence match (case-insensitive) of each command's label against the query; empty query
// returns all. Preserves input order.
export function filterCommands(items, query) {
  const q = (query || '').trim().toLowerCase()
  if (!q) return items.map((it) => ({ item: it, score: 0 }))
  const out = []
  for (const it of items) {
    const label = (it.label || '').toLowerCase()
    const hay = label + ' ' + (it.hint || '').toLowerCase()
    // 优先完全子串（score 高），否则子序列匹配
    if (hay.includes(q)) { out.push({ item: it, score: 0 }); continue }
    let i = 0
    for (const ch of q) { const idx = hay.indexOf(ch, i); if (idx < 0) { i = -1; break } i = idx + 1 }
    if (i >= 0) out.push({ item: it, score: 1 })
  }
  out.sort((a, b) => a.score - b.score)
  return out
}

/**
 * @param {{pages:Array<{to:string,label:string,hint?:string}>, onOpenStock?:(code:string,name?:string)=>void, onClose:()=>void}} props
 */
export default function CommandPalette({ pages = [], onOpenStock, onClose }) {
  const navigate = useNavigate()
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)
  const inputRef = useRef(null)
  const codeQuery = /^\d{6}$/.test(query.trim())

  // 命令 = 各页面跳转 + （六位代码时）查看个股详情
  // English: commands = one navigate action per page + (when 6-digit) open-stock-detail.
  const items = useMemo(() => {
    const list = pages.map((p) => ({
      id: p.to, label: p.label, hint: p.to, group: '页面',
      run: () => { navigate(p.to); onClose() },
    }))
    if (codeQuery && onOpenStock) {
      list.unshift({
        id: 'stock:' + query.trim(), label: `查看 ${query.trim()} 个股详情`, hint: '个股', group: '个股',
        run: () => { onOpenStock(query.trim()); onClose() },
      })
    }
    return list
  }, [pages, query, codeQuery, navigate, onOpenStock, onClose])

  const results = useMemo(() => filterCommands(items, query), [items, query])
  useEffect(() => { setActive(0) }, [query])
  useEffect(() => { inputRef.current && inputRef.current.focus() }, [])

  function onKeyDown(e) {
    if (e.key === 'Escape') { e.preventDefault(); onClose() }
    else if (e.key === 'ArrowDown') { e.preventDefault(); setActive((a) => Math.min(results.length - 1, a + 1)) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); setActive((a) => Math.max(0, a - 1)) }
    else if (e.key === 'Enter') { e.preventDefault(); const r = results[active]; if (r) r.item.run() }
  }

  return (
    <div className="cmdk-mask" data-testid="cmdk-mask" onClick={onClose}
      style={{ position: 'fixed', inset: 0, zIndex: 1200, background: 'rgba(0,0,0,0.45)', display: 'flex', alignItems: 'flex-start', justifyContent: 'center', paddingTop: '12vh' }}>
      <div data-testid="cmdk-panel" onClick={(e) => e.stopPropagation()}
        style={{ width: 'min(560px, 92vw)', background: 'var(--app-surface)', borderRadius: 12, overflow: 'hidden', boxShadow: '0 12px 48px var(--app-shadow)' }}>
        <input
          ref={inputRef}
          data-testid="cmdk-input"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={onKeyDown}
          placeholder="搜索页面或输入 6 位代码…"
          aria-label="命令搜索"
          style={{ width: '100%', border: 'none', outline: 'none', padding: '16px 18px', fontSize: 15, background: 'transparent', color: 'var(--app-text)', borderBottom: '1px solid var(--app-divider)' }}
        />
        <ul style={{ listStyle: 'none', margin: 0, padding: '6px 0', maxHeight: '56vh', overflowY: 'auto' }}>
          {results.length === 0 && <li style={{ padding: '18px', color: 'var(--app-muted)', textAlign: 'center', fontSize: 14 }}>无匹配项</li>}
          {results.map((r, idx) => (
            <li key={r.item.id} data-testid="cmdk-item"
              onMouseEnter={() => setActive(idx)} onClick={r.item.run}
              style={{
                display: 'flex', alignItems: 'center', gap: 10, padding: '10px 18px', cursor: 'pointer',
                background: idx === active ? 'var(--app-surface-2)' : 'transparent', color: 'var(--app-text)', fontSize: 14,
              }}>
              <span style={{ flex: 1 }}>{r.item.label}</span>
              {r.item.group && <span style={{ fontSize: 11, color: 'var(--app-muted-2)' }}>{r.item.group}</span>}
            </li>
          ))}
        </ul>
      </div>
    </div>
  )
}
