// ── 应用入口 main.js ──
// 负责初始化 Vue 应用、注册路由并挂载到 DOM

import { createApp } from 'vue'
import { createRouter, createWebHashHistory } from 'vue-router'
import App from './App.vue'
import Dashboard from './pages/Dashboard.vue'
import Signals from './pages/Signals.vue'
import Watchlist from './pages/Watchlist.vue'
import Positions from './pages/Positions.vue'
import Hotspot from './pages/Hotspot.vue'
import MsgCenter from './pages/MsgCenter.vue'
import Settings from './pages/Settings.vue'
import LLMDebug from './pages/LLMDebug.vue'

// 路由配置：定义所有页面路径及对应组件
const routes = [
  { path: '/', redirect: '/dashboard' },
  { path: '/dashboard', component: Dashboard },
  { path: '/signals', component: Signals },
  { path: '/watchlist', component: Watchlist },
  { path: '/positions', component: Positions },
  { path: '/hotspot', component: Hotspot },
  { path: '/msgcenter', component: MsgCenter },
  { path: '/settings', component: Settings },
  { path: '/llm-debug', component: LLMDebug },
]

// 使用 Hash 历史模式创建路由实例，避免刷新时 404
const router = createRouter({ history: createWebHashHistory(), routes })

// 创建 Vue 应用实例并挂载
const app = createApp(App)
app.use(router)
app.mount('#app')
