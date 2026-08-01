// ── 应用入口 main.js ──
// 负责初始化 Vue 应用、注册路由并挂载到 DOM
//
// 职责说明：
// 1. 集中导入并注册全部页面路由（采用 hash 模式，避免刷新时 404）；
// 2. 创建 Vue 3 应用实例，将根组件 App.vue 挂载到 index.html 的 #app 节点；
// 3. 该文件是前端启动的唯一入口：打包产物被加载后，由此拉起整个应用。
//
// 运行流程：createApp(App) -> app.use(router) -> app.mount('#app')，
// 之后由 router 根据当前 hash 命中对应页面组件，渲染到 App.vue 的 <router-view>。

// ── 依赖导入 ──
// 下列 import 引入 Vue 运行时与全站页面组件，页面组件在本文件集中注册到路由表

// Vue 3 应用工厂函数：用于创建应用实例
import { createApp } from 'vue'
// 路由工厂 createRouter + Hash 历史模式工厂 createWebHashHistory
import { createRouter, createWebHashHistory } from 'vue-router'
// 根组件：承载主布局（侧边栏 + 顶部栏 + 内容区）与登录页切换
import App from './App.vue'
// 仪表盘页：展示扫描统计、系统状态总览
import Dashboard from './pages/Dashboard.vue'
// 信号页：展示策略触发的信号列表
import Signals from './pages/Signals.vue'
// 自选股页：关注标的的查看与增删
import Watchlist from './pages/Watchlist.vue'
// 持仓页：持仓列表与可用资金管理
import Positions from './pages/Positions.vue'
// 板块热点页：当日热门板块及轮次记录
import Hotspot from './pages/Hotspot.vue'
// 消息中心页：展示提醒 / 告警消息
import MsgCenter from './pages/MsgCenter.vue'
// 设置页：服务器地址、LLM、战法参数等配置
import Settings from './pages/Settings.vue'
// LLM 诊断页：调试查看 LLM 决策的输入输出
import LLMDebug from './pages/LLMDebug.vue'

// 路由配置：定义所有页面路径及对应组件
// 每条记录由 path（URL hash 路径）与 component（渲染组件）组成
const routes = [
  // 根路径重定向到仪表盘，避免访问 / 时出现空白页
  { path: '/', redirect: '/dashboard' },
  // 仪表盘：系统状态与扫描统计总览
  { path: '/dashboard', component: Dashboard },
  // 信号：策略信号列表与买卖/忽略操作
  { path: '/signals', component: Signals },
  // 自选：自选股列表管理
  { path: '/watchlist', component: Watchlist },
  // 持仓：查看 / 更新持仓与可用资金
  { path: '/positions', component: Positions },
  // 热点：板块热点与轮次快照
  { path: '/hotspot', component: Hotspot },
  // 消息：消息中心的提醒 / 告警
  { path: '/msgcenter', component: MsgCenter },
  // 设置：各类系统配置项
  { path: '/settings', component: Settings },
  // LLM 诊断：调试 LLM 决策过程
  { path: '/llm-debug', component: LLMDebug },
]

// 使用 Hash 历史模式创建路由实例，避免刷新时 404
// 原理：createWebHashHistory 将路由状态保存在 URL 的 hash（#）部分，
// 改变 hash 不会触发浏览器向服务器发起新请求，因此刷新 / 前进后退
// 都由前端本地完成，无需服务器配置 rewrite 规则，适合纯静态部署。
const router = createRouter({ history: createWebHashHistory(), routes })

// 创建 Vue 应用实例并挂载
// createApp(App) 生成应用实例；use(router) 全局安装路由插件；
// mount('#app') 把根组件渲染进 index.html 中的 #app 元素。
const app = createApp(App)
app.use(router)
app.mount('#app')
