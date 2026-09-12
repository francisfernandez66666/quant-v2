// ── §F4 全局主题模块 theme.js ──
// 单一真相源管理浅色/深色主题：切换时同时设置 documentElement 的 `theme-mode` 属性
// （TDesign 组件由 tdesign.css 内置的 :root[theme-mode='dark'] 令牌自动翻转）与 `dark` 类
// （我们的 --app-* 令牌块也监听 :root.dark），并持久化到 localStorage、发布变更通知给订阅者
// （canvas 图表据此重绘）。默认浅色，与历史版本一致（零回归）。
// English: §F4 global theme module. Single source of truth for light/dark: toggling sets the
// documentElement `theme-mode` attribute (TDesign components flip via tdesign.css's built-in
// `:root[theme-mode='dark']` tokens) plus a `dark` class (our --app-* token block listens to it too),
// persists to localStorage, and notifies subscribers (so canvas charts repaint). Defaults to light,
// matching the previous build (zero regression).
import { useEffect, useState } from 'react'

export const THEME_KEY = 'app_theme_v1'
const VALID = new Set(['light', 'dark'])

// listeners 订阅者集合（图表等需要感知主题变化的非 React 组件）。
// English: subscriber set for consumers outside React (canvas charts).
const listeners = new Set()

function readStored() {
  try {
    const t = localStorage.getItem(THEME_KEY)
    return VALID.has(t) ? t : 'light'
  } catch (_) { return 'light' }
}

/** 当前主题（'light' | 'dark'）。English: current theme id. */
export function getTheme() {
  return (typeof document !== 'undefined' && document.documentElement.getAttribute('theme-mode') === 'dark') ? 'dark' : 'light'
}

// apply 把主题写进 DOM 属性/类并广播，不关心 React 状态。English: apply a theme to the DOM and notify.
function apply(t) {
  const theme = VALID.has(t) ? t : 'light'
  if (typeof document !== 'undefined') {
    const root = document.documentElement
    if (theme === 'dark') root.setAttribute('theme-mode', 'dark')
    else root.removeAttribute('theme-mode')
    root.classList.toggle('dark', theme === 'dark')
  }
  listeners.forEach((fn) => { try { fn(theme) } catch (_) {} })
  return theme
}

/**
 * 设置主题（持久化 + 应用 + 广播）。
 * @param {'light'|'dark'} t
 * @returns {'light'|'dark'} 实际生效主题
 */
export function setTheme(t) {
  const theme = apply(t)
  try { localStorage.setItem(THEME_KEY, theme) } catch (_) {}
  return theme
}

/** 在 light/dark 间取反并返回新值。English: toggle and return the new theme. */
export function toggleTheme() {
  return setTheme(getTheme() === 'dark' ? 'light' : 'dark')
}

/**
 * 订阅主题变化（返回取消订阅函数）。供 canvas 图表在非 React 语境下重绘。
 * English: subscribe to theme changes (returns an unsubscribe fn) for canvas repaints.
 */
export function subscribeTheme(fn) {
  listeners.add(fn)
  return () => listeners.delete(fn)
}

/**
 * 应用启动时读取持久化主题并落到 DOM（在 main.jsx render 之前调用，避免闪烁）。
 * English: read the persisted theme and apply it to the DOM at boot (called before render).
 */
export function initTheme() {
  return apply(readStored())
}

/**
 * useTheme React 钩子：返回 [theme, setThemeFn, toggle]，内部监听广播保持多组件同步。
 * @returns {['light'|'dark', (t:string)=>void, ()=>void]}
 */
export function useTheme() {
  const [theme, setThemeState] = useState(getTheme)
  useEffect(() => {
    const off = subscribeTheme((t) => setThemeState(t))
    setThemeState(getTheme())
    return off
  }, [])
  return [theme, (t) => setThemeState(setTheme(t)), () => setThemeState(toggleTheme())]
}
