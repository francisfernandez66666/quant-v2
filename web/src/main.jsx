// ── 应用入口 main.jsx ──
// React 版入口：挂载根组件 App，启用 HashRouter（静态部署刷新不 404）+ TDesign 浅色主题。
import React from 'react'
import { createRoot } from 'react-dom/client'
import { HashRouter } from 'react-router-dom'
import App from './App.jsx'
import { initTheme } from './theme.js'
import { installGlobalErrorReporting } from './errorReporter.js'

// §F4 首屏前应用持久化主题，避免浅/深色闪烁（FOUC）。
// English: apply the persisted theme before first paint to avoid a light/dark flash.
initTheme()
// §OBSERVABILITY：登录后的运行期错误上报到后端 opslog/用量看板（未登录自动跳过）。
installGlobalErrorReporting()

// TDesign React 全量样式；浅色为默认主题，无需额外激活
import 'tdesign-react/dist/tdesign.css'
// 应用全局样式（外壳布局/响应式/页面容器/登录页），须置于 TDesign 之后以便覆盖
import './styles.css'

createRoot(document.getElementById('app')).render(
  <React.StrictMode>
    <HashRouter>
      <App />
    </HashRouter>
  </React.StrictMode>
)
