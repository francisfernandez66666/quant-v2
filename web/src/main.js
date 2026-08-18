// ── 应用入口 main.js ──
// ── App entry point main.js ──
// 负责初始化 Vue 应用、注册路由并挂载到 DOM
// Initializes the Vue app, registers the router, and mounts to the DOM
//
// 职责说明：
// Responsibilities:
// 1. 集中导入并注册全部页面路由（采用 hash 模式，避免刷新时 404）；
// 1. Centrally imports and registers all page routes (hash mode, so a refresh never 404s);
// 2. 创建 Vue 3 应用实例，将根组件 App.vue 挂载到 index.html 的 #app 节点；
// 2. Creates the Vue 3 app instance and mounts the root component App.vue onto the #app node of index.html;
// 3. 该文件是前端启动的唯一入口：打包产物被加载后，由此拉起整个应用。
// 3. This file is the single entry point: once the bundle is loaded, the whole app starts here.
//
// 运行流程：createApp(App) -> app.use(router) -> app.mount('#app')，
// Flow: createApp(App) -> app.use(router) -> app.mount('#app'),
// 之后由 router 根据当前 hash 命中对应页面组件，渲染到 App.vue 的 <router-view>。
// afterwards the router matches the current hash to a page component, rendered inside App.vue's <router-view>.

// ── 依赖导入 ──
// ── Imports ──
// 下列 import 引入 Vue 运行时与全站页面组件，页面组件在本文件集中注册到路由表
// The imports below pull in the Vue runtime and all page components; these components are registered here into the route table

// Vue 3 应用工厂函数：用于创建应用实例
// Vue 3 'createApp' factory: creates the app instance
import { createApp } from 'vue'
// 路由工厂 createRouter + Hash 历史模式工厂 createWebHashHistory
// Router factory createRouter plus the hash-history factory createWebHashHistory
import { createRouter, createWebHashHistory } from 'vue-router'
// 根组件：承载主布局（侧边栏 + 顶部栏 + 内容区）与登录页切换
// Root component: hosts the main layout (sidebar + topbar + content) and the login page switch
import App from './App.vue'
// 仪表盘页：展示扫描统计、系统状态总览
// Dashboard page: scan stats and system status overview
import Dashboard from './pages/Dashboard.vue'
// 信号页：展示策略触发的信号列表
// Signals page: list of strategy-triggered signals
import Signals from './pages/Signals.vue'
// 自选股页：关注标的的查看与增删
// Watchlist page: view and add/remove watched stocks
import Watchlist from './pages/Watchlist.vue'
// 持仓页：持仓列表与可用资金管理
// Positions page: holdings list and available-cash management
import Positions from './pages/Positions.vue'
// 板块热点页：当日热门板块及轮次记录
// Hotspot page: today's hot sectors and rotation records
import Hotspot from './pages/Hotspot.vue'
// 消息中心页：展示提醒 / 告警消息
// Message center page: reminder / alert messages
import MsgCenter from './pages/MsgCenter.vue'
// 设置页：服务器地址、LLM、战法参数等配置
// Settings page: server URL, LLM, strategy parameter config
import Settings from './pages/Settings.vue'
// LLM 诊断页：调试查看 LLM 决策的输入输出
// LLM debug page: inspect the inputs/outputs of LLM decisions
import LLMDebug from './pages/LLMDebug.vue'
// 股票咨询页：多轮 LLM 对话咨询
// Consult page: multi-turn LLM Q&A consultation
import Consult from './pages/Consult.vue'
// 自动研究页：B5 优化候选审批与应用
// Auto-research page: B5 optimizer candidate approval and application
import Research from './pages/Research.vue'
// 用户管理页：账号开通/权限配置（仅 admin）
// Admin page: account creation and permission management (admin only)
import Admin from './pages/Admin.vue'

// 路由配置：定义所有页面路径及对应组件
// Route config: defines every page path and its component
// 每条记录由 path（URL hash 路径）与 component（渲染组件）组成
// Each entry consists of a path (URL hash path) and a component (rendered view)
const routes = [
  // 根路径重定向到仪表盘，避免访问 / 时出现空白页
  // Redirect the root path to the dashboard to avoid a blank page at /
  { path: '/', redirect: '/dashboard' },
  // 仪表盘：系统状态与扫描统计总览
  // Dashboard: system status and scan stats
  { path: '/dashboard', component: Dashboard },
  // 信号：策略信号列表与买卖/忽略操作
  // Signals: strategy signal list with buy/ignore actions
  { path: '/signals', component: Signals },
  // 自选：自选股列表管理
  // Watchlist: manage the watchlist
  { path: '/watchlist', component: Watchlist },
  // 持仓：查看 / 更新持仓与可用资金
  // Positions: view / update holdings and available cash
  { path: '/positions', component: Positions },
  // 热点：板块热点与轮次快照
  // Hotspot: sector hotspots and rotation snapshots
  { path: '/hotspot', component: Hotspot },
  // 消息：消息中心的提醒 / 告警
  // Messages: message center reminders / alerts
  { path: '/msgcenter', component: MsgCenter },
  // 设置：各类系统配置项
  // Settings: assorted system configuration
  { path: '/settings', component: Settings },
  // LLM 诊断：调试 LLM 决策过程
  // LLM debug: debug the LLM decision process
  { path: '/llm-debug', component: LLMDebug },
  // 股票咨询：多轮 LLM 对话
  // Consult: multi-turn LLM chat
  { path: '/consult', component: Consult },
  // 自动研究：B5 候选审批与应用
  // Auto-research: B5 candidate approval and application
  { path: '/research', component: Research },
  // 用户管理：账号开通/权限配置（仅 admin）
  // Admin: account creation and permission management (admin only)
  { path: '/admin', component: Admin },
]

// 使用 Hash 历史模式创建路由实例，避免刷新时 404
// Create the router with hash history to avoid 404s on refresh
// 原理：createWebHashHistory 将路由状态保存在 URL 的 hash（#）部分，
// Why: createWebHashHistory keeps routing state in the hash (#) part of the URL,
// 改变 hash 不会触发浏览器向服务器发起新请求，因此刷新 / 前进后退
// changing the hash never triggers a new server request, so refresh / back-forward
// 都由前端本地完成，无需服务器配置 rewrite 规则，适合纯静态部署。
// are handled entirely client-side without server rewrite rules, ideal for static hosting.
const router = createRouter({ history: createWebHashHistory(), routes })

// 创建 Vue 应用实例并挂载
// Create and mount the Vue app
// createApp(App) 生成应用实例；use(router) 全局安装路由插件；
// createApp(App) builds the instance; use(router) installs the router plugin globally;
// mount('#app') 把根组件渲染进 index.html 中的 #app 元素。
// mount('#app') renders the root component into the #app element of index.html.
const app = createApp(App)
app.use(router)
app.mount('#app')
