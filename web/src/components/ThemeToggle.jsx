// ── §F4 主题切换按钮 ThemeToggle.jsx ──
// 顶栏一个图标按钮，点击在浅/深色间切换（写 localStorage + documentElement + 广播）。
// English: header icon button toggling light/dark (persists, applies to the DOM, broadcasts).
import React from 'react'
import { Button } from 'tdesign-react'
import { useTheme } from '../theme.js'

export default function ThemeToggle() {
  const [theme, , toggle] = useTheme()
  return (
    <Button theme="default" variant="outline" size="small" aria-label="切换主题"
      title={theme === 'dark' ? '切换到浅色' : '切换到深色'}
      onClick={toggle}>
      {theme === 'dark' ? '☀' : '☾'}
    </Button>
  )
}
