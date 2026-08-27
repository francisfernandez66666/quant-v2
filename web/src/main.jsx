// ── 应用入口 main.jsx ──
// React 版入口：挂载根组件 App，启用 HashRouter（静态部署刷新不 404）+ TDesign 暗色主题。
import React from 'react'
import { createRoot } from 'react-dom/client'
import { HashRouter } from 'react-router-dom'
import App from './App.jsx'

// TDesign React 全量样式；暗色主题通过 <html theme-mode="dark"> 激活
import 'tdesign-react/dist/tdesign.css'
// 应用全局样式（外壳布局/响应式/页面容器/登录页），须置于 TDesign 之后以便覆盖
import './styles.css'

// 激活 TDesign 暗色主题（与旧 Vue 版 #0f0f23 深色 UI 一致）
document.documentElement.setAttribute('theme-mode', 'dark')

createRoot(document.getElementById('app')).render(
  <React.StrictMode>
    <HashRouter>
      <App />
    </HashRouter>
  </React.StrictMode>
)
