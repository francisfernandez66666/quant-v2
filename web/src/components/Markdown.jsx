// ── 轻量 Markdown 渲染器 Markdown.jsx ──
// 仅支持咨询回复用到的「安全子集」，输出 React 元素（不使用 dangerouslySetInnerHTML，杜绝 XSS）。
// 支持：## / ### 标题、- / * 无序列表、1. 有序列表、**粗体**、*斜体*、`行内代码`、段落与软换行。
// 设计取舍：不引入第三方 markdown 库（避免新增依赖与构建风险），自写最小解析，且全程返回
// React 节点而非拼接 HTML 字符串——任何用户输入都无法注入标签。
import React from 'react'

const rootStyle = { fontSize: 14, lineHeight: 1.62, wordBreak: 'break-word' }
const headingStyle = (level) =>
  level === 2
    ? { fontWeight: 600, fontSize: 15, margin: '10px 0 4px', paddingLeft: 8, borderLeft: '3px solid var(--td-brand-color)' }
    : { fontWeight: 600, fontSize: 14, margin: '8px 0 3px' }
const pStyle = { margin: '4px 0' }
const listStyle = { margin: '4px 0', paddingLeft: 18 }
const liStyle = { margin: '2px 0' }
const codeStyle = {
  background: 'var(--app-surface-3, rgba(125,125,125,0.16))',
  padding: '0 4px',
  borderRadius: 4,
  fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
  fontSize: 13,
}

// renderInline：解析行内格式（粗体/斜体/代码），返回 React 节点数组，绝不直接拼接 HTML。
function renderInline(text, keyPrefix) {
  const re = /(\*\*[^*]+\*\*)|(`[^`]+`)|(\*[^*]+\*)/g
  const out = []
  let last = 0
  let m
  let idx = 0
  while ((m = re.exec(text)) !== null) {
    if (m.index > last) out.push(<span key={keyPrefix + '-t' + idx++}>{text.slice(last, m.index)}</span>)
    const tok = m[0]
    if (tok.startsWith('**')) {
      out.push(<strong key={keyPrefix + '-b' + idx++}>{tok.slice(2, -2)}</strong>)
    } else if (tok.startsWith('`')) {
      out.push(<code key={keyPrefix + '-c' + idx++} style={codeStyle}>{tok.slice(1, -1)}</code>)
    } else {
      out.push(<em key={keyPrefix + '-e' + idx++}>{tok.slice(1, -1)}</em>)
    }
    last = m.index + tok.length
  }
  if (last < text.length) out.push(<span key={keyPrefix + '-t' + idx++}>{text.slice(last)}</span>)
  return out
}

// Markdown：把 LLM 回复文本渲染为带层级结构的 React 节点。
export default function Markdown({ text }) {
  if (!text) return null
  const lines = text.split('\n')
  const blocks = []
  let i = 0
  let key = 0

  while (i < lines.length) {
    const line = lines[i]
    // 空行：仅作段落分隔，不产出节点
    if (line.trim() === '') {
      i++
      continue
    }
    // 标题 ## / ###
    const h = /^(#{2,3})\s+(.*)$/.exec(line)
    if (h) {
      const level = h[1].length
      const Tag = level === 2 ? 'h3' : 'h4'
      blocks.push(
        <Tag key={key++} style={headingStyle(level)}>
          {renderInline(h[2], 'h' + key)}
        </Tag>,
      )
      i++
      continue
    }
    // 无序列表 - / *
    if (/^\s*[-*]\s+/.test(line)) {
      const items = []
      while (i < lines.length && /^\s*[-*]\s+/.test(lines[i])) {
        items.push(
          <li key={'li' + key + '-' + items.length} style={liStyle}>
            {renderInline(lines[i].replace(/^\s*[-*]\s+/, ''), 'li' + key + items.length)}
          </li>,
        )
        i++
      }
      blocks.push(
        <ul key={key++} style={listStyle}>
          {items}
        </ul>,
      )
      continue
    }
    // 有序列表 1. 2. ...
    if (/^\s*\d+\.\s+/.test(line)) {
      const items = []
      while (i < lines.length && /^\s*\d+\.\s+/.test(lines[i])) {
        items.push(
          <li key={'ol' + key + '-' + items.length} style={liStyle}>
            {renderInline(lines[i].replace(/^\s*\d+\.\s+/, ''), 'ol' + key + items.length)}
          </li>,
        )
        i++
      }
      blocks.push(
        <ol key={key++} style={listStyle}>
          {items}
        </ol>,
      )
      continue
    }
    // 段落：聚合连续的非结构化行，行内换行用 <br> 保留
    const para = []
    while (
      i < lines.length &&
      lines[i].trim() !== '' &&
      !/^(#{2,3})\s+/.test(lines[i]) &&
      !/^\s*[-*]\s+/.test(lines[i]) &&
      !/^\s*\d+\.\s+/.test(lines[i])
    ) {
      para.push(lines[i])
      i++
    }
    blocks.push(
      <p key={key++} style={pStyle}>
        {para.map((p, pi) => (
          <React.Fragment key={pi}>
            {pi > 0 && <br />}
            {renderInline(p, 'p' + key + pi)}
          </React.Fragment>
        ))}
      </p>,
    )
  }

  return <div style={rootStyle}>{blocks}</div>
}
