// ── 用户管理页 Admin.jsx（仅 admin 可见）──
// Admin page (admin only): account creation, role/perm config, password reset,
// enable/disable, expiry, and per-account strategy param delegation.
import React, { useState, useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import {
  Button, Input, InputNumber, Select, Dialog,
  Table, Tag, Card, Form, Checkbox,
} from 'tdesign-react'
import ToggleSw from '../components/ToggleSw'
import * as api from '../api/index.js'
import { showToast, confirmDialog } from '../ui.jsx'

// 从 api 模块导入权限判断工具：isAdmin() 读取 localStorage 中缓存的 role（由 App 的 refreshMe 写入）
// 守卫判断来源：web/src/api/index.js 的 isAdmin()（基于 STORAGE_ROLE），与 App.jsx 中侧边栏 canAdmin 一致
import { isAdmin } from '../api/index.js'
// §P2-I（2026-10-06 修复批 波 6）轮询「后到丢弃」统一入口：本文件 30s 轮询的运行日志正文
// 由「定时器 + 日期切换 + 手动刷新」三驱动交错，上一轮在途时下一轮已发出，旧响应迟到会把
// 刚刷出来的日志盖回上一轮值——用这一个 hook 持有跨渲染稳定的守卫实例（内部 useRef 惰性初始化，
// 严禁写成 useRef(createStaleGuard())：那样每渲染 new 一个、代号序列重置、守卫恒不判红=假绿）。
import { useStaleGuard } from '../utils/staleGuard.js' // §P2-I 轮询后到丢弃（统一 hook）
// §P2-J 同族（吞错可见化）：读取腿失败按腿名进共用台账，页顶出红条点名「哪条腿没读到」，
// 绝不再用上一轮读数或空列表冒充「最新/没有」。
import { useLoadLedger } from '../utils/loadLedger.js' // §P2-J 读取失败按腿记账（共用台账）
// §P2-J 台账红条单实现：页面只管记账与摆放位置，文案/testid 都在组件里，不在页内抄 div。
import LoadFailBanner from '../components/LoadFailBanner.jsx' // §P2-J 台账红条（单实现，testid 只有一处）

// 权限位中文标签映射：把后端下发的英文权限标识翻译为界面可读文案（当前仅"研究审批"一项）
const PERM_LABELS = { research_approve: '研究审批' }

// 战法参数分组定义（与 Vue 版 Settings/Admin 一致）；每个 group 的 fields 决定代配弹窗中展示的输入框与步长
const strategyGroups = [
  {
    // ── 龙头战法：涨停打板核心因子权重与卖出/止盈参数，管理员可代任意账号下发 ──
    key: 'dragon', title: '龙头战法（权重合计≤1）',
    fields: [
      // 打分四因子权重（合计≤1，决定候选股排序优先级）
      // F1 首封质量因子权重
      { k: 'f1_seal_weight', label: 'F1 首封权重', step: 0.05 },
      // F2 板块共振因子权重
      { k: 'f2_resonance_weight', label: 'F2 共振权重', step: 0.05 },
      // F3 次日溢价预期因子权重
      { k: 'f3_premium_weight', label: 'F3 溢价权重', step: 0.05 },
      // F4 相对强度(RS)因子权重
      { k: 'f4_rs_weight', label: 'F4 强度权重', step: 0.05 },
      // 卖出风控与止盈参数（回撤/炸板分级减仓、收盘与次日开盘走弱判定、止盈）
      // 持仓期允许的最大回撤
      { k: 'pullback_max_pct', label: '最大回撤%', step: 0.01 },
      // 炸板回撤达该值减半仓
      { k: 'breaker_sell_half_pct', label: '炸板减半%', step: 0.01 },
      // 炸板回撤达该值清仓
      { k: 'breaker_sell_all_pct', label: '炸板清仓%', step: 0.01 },
      // 买入后回撤减半阈值
      { k: 'buy_pullback_sell_half_pct', label: '买入回撤减半%', step: 0.01 },
      // 买入后回撤清仓阈值
      { k: 'buy_pullback_sell_all_pct', label: '买入回撤清仓%', step: 0.01 },
      // 买入日收盘走弱判定
      { k: 'buy_day_close_below', label: '买入日收盘低于%', step: 0.01 },
      // 次日开盘走弱判定
      { k: 'next_open_if_below', label: '次日开盘低于%', step: 0.01 },
      // 止盈幅度
      { k: 'take_profit_pct', label: '止盈%', step: 1 },
    ],
  },
  {
    // ── 双响炮战法：两段放量突破形态的量比与评分权重参数 ──
    key: 'double_bump', title: '双响炮战法',
    fields: [
      // 第一次突破要求的量比
      { k: 'first_break_volume_multiple', label: '一突量比', step: 0.1 },
      // 第二次突破要求的量比
      { k: 'second_break_volume_multiple', label: '二突量比', step: 0.1 },
      // 突破间调整期量比上限
      { k: 'adjust_vol_ratio_max', label: '调整量比上限', step: 0.5 },
      // 调整深度评分权重
      { k: 'position_weight', label: '调整深度权重', step: 0.05 },
      // 均线形态评分权重
      { k: 'ma_weight', label: '均线权重', step: 0.05 },
      // 量能评分权重
      { k: 'volume_weight', label: '量能权重', step: 0.05 },
      // 止盈幅度
      { k: 'double_bump_take_profit_pct', label: '止盈%', step: 0.01 },
    ],
  },
  {
    // ── N 形战法：形态分门槛与硬止损 ──
    key: 'n_shape', title: 'N 形战法',
    fields: [
      // N 形态分达该阈值才出信号
      { k: 'n_pattern_score_threshold', label: 'N 形态分阈值', step: 1 },
      // 固定硬止损幅度
      { k: 'hard_stop_loss', label: '硬止损%', step: 0.01 },
    ],
  },
  {
    // ── 龙回头战法：强势股回调低吸的止损止盈与分批目标参数 ──
    key: 'dragon_return', title: '龙回头战法',
    fields: [
      // 止损幅度
      { k: 'stop_loss_pct', label: '止损%', step: 0.01 },
      // 止盈幅度
      { k: 'take_profit_pct', label: '止盈%', step: 0.01 },
      // 超期强制离场
      { k: 'max_hold_days', label: '最长持仓天数', step: 1 },
      // 第一目标位（ATR 倍数）
      { k: 'target1_multiplier', label: '目标1倍数', step: 0.05 },
      // 第二目标位（ATR 倍数）
      { k: 'target2_multiplier', label: '目标2倍数', step: 0.05 },
      // 浮盈回撤比例触发移动止损
      { k: 'trailing_drawback', label: '移动止损回撤%', step: 0.01 },
    ],
  },
  {
    // ── 动量分模型：量价/MACD/走势三因子权重与动量闸门（信号过滤开关） ──
    // 注：动量"能不能交易"由战法开关白名单统一裁决（§SIGNAL_CONTROLLER），不在此参数页。
    key: 'momentum', title: '动量分权重（合计建议=100）',
    fields: [
      // 量价配合因子权重
      { k: 'volume_price_weight', label: '量价权重', step: 5 },
      // MACD 动量因子权重
      { k: 'macd_weight', label: 'MACD权重', step: 5 },
      // 走势趋势因子权重
      { k: 'trend_weight', label: '走势权重', step: 5 },
      { k: 'momentum_gate_enabled', label: '动量提升才提醒', type: 'switch', hint: '开启后仅当动量分提升(或回落≤容忍差)才放行 双响炮/龙头/龙回头 战法信号；N形不受影响' },
      { k: 'momentum_delta_tol', label: '回落容忍差(分)', step: 1, hint: '动量分相对上一轮回落 ≤ 该值仍视为提升；设为0表示需严格不回落' },
    ],
  },
]

// 将权限标识翻译为中文
function permLabel(p) {
  return PERM_LABELS[p] || p
}

// 时间戳格式化为 YYYY-MM-DD
function fmtTime(ts) {
  if (!ts) return '-'
  const d = new Date(ts * 1000)
  // 两位补零（月/日展示）
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

// 根据用户有效期生成中文说明（永久 / 已到期 / 剩余天数）
function expiryText(u) {
  if (!u.expires_at) return '有效期：永久'
  const now = Date.now()
  const exp = u.expires_at * 1000
  if (exp < now) return '有效期：已到期'
  // 86400000 = 一天的毫秒数，用于换算剩余天数
  const days = Math.ceil((exp - now) / 86400000)
  return '有效期：' + fmtTime(u.expires_at) + '（剩 ' + days + ' 天）'
}


/**
 * 用户管理页面组件（仅 admin 可见）
 * 负责账号创建、角色/权限/有效期管理、密码重置与战法参数下发。
 * @returns {JSX.Element}
 */
export default function Admin() {
  // §A5（20260918 审计批）：数据首拉 403（本地角色缓存与服务端权威角色不一致，
  // 如会话中途被降权）时统一重路由 /403，而非渲染错误 Toast。
  const navigate = useNavigate()
  const [users, setUsers] = useState([])
  const [allPerms, setAllPerms] = useState([])
  const [creating, setCreating] = useState(false)
  const [createMsg, setCreateMsg] = useState('')
  const [createMsgType, setCreateMsgType] = useState('ok')
  const [newUser, setNewUser] = useState({ username: '', password: '', role: 'user', perms: [], expiresDays: 0, permanent: true, tenantId: '' })
  // §MT 多租户状态：tenant_names=ID→名称映射；platform=当前账号是否平台运营者；
  // tenants=租户清单（含配额/用量）；tenantForm=新建租户表单。
  const [tenantNames, setTenantNames] = useState({})
  const [platform, setPlatform] = useState(false)
  const [tenants, setTenants] = useState([])
  const [tenantForm, setTenantForm] = useState({ name: '', maxUsers: 20, apiRate: 600, adminUser: '', adminPass: '' })
  const [tenantSaving, setTenantSaving] = useState(false)

  const [activeUser, setActiveUser] = useState(null)
  const [activeStrategy, setActiveStrategy] = useState({})
  const [strategySaving, setStrategySaving] = useState(false)
  const [strategyMsg, setStrategyMsg] = useState('')
  const [strategyMsgType, setStrategyMsgType] = useState('ok')

  // 重置密码弹窗状态
  const [pwUser, setPwUser] = useState(null)
  const [pwValue, setPwValue] = useState('')
  // 设置有效期弹窗状态
  const [expUser, setExpUser] = useState(null)
  const [expDays, setExpDays] = useState(0)

  // ── §DAILY_OPSLOG 系统运行日志（管理员）──
  const [opsDates, setOpsDates] = useState([])      // 可选日期（倒序，"20260831"）
  const [opsDate, setOpsDate] = useState('')        // 当前选中日期
  const [opsLines, setOpsLines] = useState([])      // 当日日志行
  const [opsMeta, setOpsMeta] = useState({ total: 0, truncated: false })
  // §D6 修复：账号列表分页受控——旧写法用 `defaultPageSize:10` 非受控，
  // 用户切到 50/100 后 refresh / 增删 / 角色变更触发重渲染就掉回 10。
  // English: D6 — controlled pagination so pageSize survives re-renders.
  const [userPage, setUserPage] = useState({ current: 1, pageSize: 10 })
  // §U-5 脏账号清理请求中标志（防重复点击）
  const [cleaning, setCleaning] = useState(false)
  const [opsLoading, setOpsLoading] = useState(false)
  const opsBodyRef = React.useRef(null)             // 内容区（自动滚到底部=最新事件）
  // §P2-I（2026-10-06 修复批 波 6）：运行日志正文守卫。loadOpslog 同时被 30s 定时器、opsDate
  // 变化 effect、手动刷新三处驱动，彼此交错；loadOpslog 里 setInterval 持的是挂载期闭包，
  // 若上一轮 fetchOpslog 还在途、下一轮已 begin，旧响应迟到会整包覆盖 opsLines/opsMeta（数据倒挂，
  // 一直显示到下一次轮询）。api 层 AbortController 只管超时、管不了这种交错，故必须在写 state 前判后到。
  const opslogGuard = useStaleGuard()
  // §P2-I：日期清单单独一个守卫实例——它与正文的定时器/手动刷新各自节奏、写的是不同 state
  // （opsDates vs opsLines），两条腿各持独立代号序列，互不干扰更稳。
  const opsDatesGuard = useStaleGuard()
  // §P2-J：读取腿失败台账。本文件把每条「数据读取腿」的失败按腿名记一格、成功即销案；
  // 台账实现收在 utils/loadLedger.js 同一份 hook（红条在位锁据此只认一个 testid 常量）。
  const { fails: loadFails, mark: markLoadFail, clear: clearLoadFail } = useLoadLedger()

  // 安全读取用户权限数组
  function uPerms(u) {
    return Array.isArray(u.perms) ? u.perms : []
  }

  // 加载全部用户与权限列表（仅管理员调用；非管理员直接返回，避免越权请求）
  // §P2-I 判定（这条不接守卫）：loadUsers 不在任何 setInterval / SSE / 事件总线的驱动路径上——
  // 它只在挂载 useEffect 首拉一次，以及建号/删号/改角色后由用户手动动作触发重取。守卫是给
  // 「周期或事件驱动、会自相覆盖的轮询腿」用的；一次性手动取数不存在「上一轮在途、下一轮又发出」
  // 的交错窗口，接了反而是空转代号，故此处判为不接。
  async function loadUsers() {
    // 守卫二次校验：不是管理员则直接中止数据加载
    if (!isAdmin()) return
    try {
      const res = await api.fetchAdminUsers()
      setUsers(res.users || [])
      setAllPerms(res.perms || [])
      setTenantNames(res.tenant_names || {})
      setPlatform(!!res.platform)
      clearLoadFail('用户列表') // §P2-J：本轮成功即销案
      if (res.platform) loadTenants()
    } catch (e) {
      // §A5：首拉即 403=服务端权威角色已非管理员，跳统一 403 页而非停留在错误提示
      // 该 403 分支直接重路由（页面随即卸载），记账无意义，故只在落到下面普通失败时进台账。
      if (api.isForbidden(e)) { navigate('/403'); return }
      showToast('加载用户失败: ' + (e.message || e), 'error')
      markLoadFail('用户列表', (e && e.message) || String(e)) // §P2-J：读取失败点名，红条标出「非最新」
    }
  }

  // §MT 租户清单加载（平台运营者专用）
  // §P2-I 判定同 loadUsers：仅平台视角首拉与租户增删改的手动动作触发，无周期/SSE 驱动，不接守卫。
  async function loadTenants() {
    try {
      const res = await api.fetchTenants()
      setTenants(res.tenants || [])
      clearLoadFail('租户列表') // §P2-J：成功销案
    } catch (e) {
      showToast('加载租户失败: ' + (e.message || e), 'error')
      markLoadFail('租户列表', (e && e.message) || String(e)) // §P2-J：读取失败点名
    }
  }

  // §MT 创建租户（可选随建租户管理员）
  async function createTenant() {
    if (!tenantForm.name.trim()) { showToast('租户名称必填', 'error'); return }
    setTenantSaving(true)
    try {
      const body = {
        name: tenantForm.name.trim(),
        quota: { max_users: Number(tenantForm.maxUsers) || 0, api_rate_per_min: Number(tenantForm.apiRate) || 0 },
      }
      if (tenantForm.adminUser) body.admin = { username: tenantForm.adminUser, password: tenantForm.adminPass }
      await api.createTenant(body)
      showToast('租户已创建')
      setTenantForm({ name: '', maxUsers: 20, apiRate: 600, adminUser: '', adminPass: '' })
      loadTenants(); loadUsers()
    }
    // §P2-J 可吞：创建租户是写操作，失败已由 Toast 即时反馈给发起人（谁点谁当场看到），
    // 不是无人触发的后台周期读数腿，无须进读取台账冒充「上一轮读数」——台账只登记周期/事件驱动的读取失败。
    catch (e) {
      showToast('创建租户失败: ' + (e.message || e), 'error')
    } finally { setTenantSaving(false) }
  }

  // §MT 启停租户 / 调整配额
  async function toggleTenantEnabled(t) {
    if (!(await confirmDialog(`${t.enabled ? '停用' : '启用'}租户「${t.name}」？停用后该租户禁止新增成员。`, '租户操作'))) return
    try {
      await api.updateTenant(t.id, { enabled: !t.enabled })
      showToast('已更新'); loadTenants()
    }
    // §P2-J 可吞：启停租户是写操作，失败即时 Toast 反馈发起人，非周期读数腿。
    catch (e) { showToast('操作失败: ' + (e.message || e), 'error') }
  }

  // §MT 调整租户配额：弹窗依次询问成员上限与每分钟 API 限流（0=恢复系统默认值）
  async function editTenantQuota(t) {
    const mv = prompt(`「${t.name}」成员上限（当前 ${t.max_users}）`, String(t.max_users))
    if (mv === null) return
    const ar = prompt(`每分钟 API 限流（当前 ${t.api_rate_per_min}，0=默认600）`, String(t.api_rate_per_min))
    if (ar === null) return
    try {
      await api.updateTenant(t.id, { quota: { max_users: Number(mv) || 0, api_rate_per_min: Number(ar) || 0 } })
      showToast('配额已更新'); loadTenants()
    }
    // §P2-J 可吞：改租户配额是写操作，失败即时 Toast 反馈发起人，非周期读数腿。
    catch (e) { showToast('更新失败: ' + (e.message || e), 'error') }
  }

  // §U-5（2026-09-14 像素级 UAT）脏账号清理：先 dry_run 预览命中清单，确认后再真删。
  // 后端口径保守（过期非管理员账号 + 会话全部过期/已禁用的 temp_ 临时号），admin 与在用号绝不误删。
  // English: §U-5 stale-account reaper — preview via dry_run, confirm, then delete. Backend policy
  // is conservative (expired non-admins + dead/disabled temp accounts); never touches admin/live rows.
  async function runUserCleanup() {
    setCleaning(true)
    try {
      const preview = await api.cleanupAdminUsers(true)
      const list = preview.deleted || []
      if (!list.length) { showToast('没有可清理的账号（无过期/temp 僵尸号）'); return }
      const names = list.slice(0, 8).map((x) => x.username).join('、') + (list.length > 8 ? ` 等 ${list.length} 个` : '')
      if (!(await confirmDialog(`将清理 ${list.length} 个僵尸账号：${names}。\n（过期或已失效临时号，admin 与在用账号不会受影响）`, '清理账号确认'))) return
      const done = await api.cleanupAdminUsers(false)
      showToast(`已清理 ${done.count || 0} 个账号`)
      loadUsers()
    }
    // §P2-J 可吞：账号清理是写操作（先 dry_run 预览再真删），失败即时 Toast 反馈发起人，非周期读数腿。
    catch (e) {
      showToast('清理失败: ' + (e.message || e), 'error')
    } finally {
      setCleaning(false)
    }
  }

  // 创建新账号并清空表单
  function createUser() {
    // §F31 修复：提交前预校验，与后端 auth.CreateUser 的入参约束对齐——
    // 用户名 3-20 位（首字符字母，允许字母/数字/下划线）；密码 ≥8 位。
    // 后端目前只在 auth.go 层校验（撞车用户名/密码过短），错误信息经 catch 冒泡到 createMsg；
    // 这里前端先拦，错误直白，避免用户按下"创建"才被拒。
    if (!newUser.username || !newUser.password) {
      setCreateMsg('用户名和密码必填'); setCreateMsgType('err'); return
    }
    if (!/^[A-Za-z][A-Za-z0-9_]{2,19}$/.test(newUser.username)) {
      setCreateMsg('用户名需 3-20 位，首字符为字母，仅允许字母/数字/下划线')
      setCreateMsgType('err'); return
    }
    if (newUser.password.length < 8) {
      setCreateMsg('初始密码至少 8 位')
      setCreateMsgType('err'); return
    }
    setCreating(true); setCreateMsg('')
    api.createAdminUser({
      username: newUser.username,
      password: newUser.password,
      role: newUser.role,
      perms: newUser.perms,
      // 有效期天数：勾选"永久"时传 0 表示永不过期
      expires_days: newUser.permanent ? 0 : (newUser.expiresDays || 0),
      // §MT 平台运营者可指定目标租户；租户 admin 不传（后端强制本租户）
      tenant_id: platform && newUser.tenantId ? newUser.tenantId : undefined,
    }).then(() => {
      setCreateMsg('账号已创建'); setCreateMsgType('ok')
      setNewUser({ username: '', password: '', role: 'user', perms: [], expiresDays: 0, permanent: true, tenantId: '' })
      loadUsers()
    })
    // §P2-J 可吞：建号是写操作，失败内联回表单提示（createMsg）给发起人，非周期读数腿，不进台账。
    .catch((e) => {
      setCreateMsg('创建失败: ' + (e.message || e)); setCreateMsgType('err')
    }).finally(() => setCreating(false))
  }

  // 切换用户 admin/user 角色
  async function toggleRole(u) {
    const role = u.role === 'admin' ? 'user' : 'admin'
    try {
      await api.setAdminUserRole(u.id, role)
      setUsers(users.map((x) => x.id === u.id ? { ...x, role } : x))
    }
    // §P2-J 可吞：角色升降是写操作，失败即时 Toast 反馈发起人，非周期读数腿。
    catch (e) {
      showToast('操作失败: ' + (e.message || e), 'error')
    }
  }

  // 全量更新某用户权限位
  async function setUserPerms(u, next) {
    try {
      await api.setAdminUserPerms(u.id, next)
      setUsers(users.map((x) => x.id === u.id ? { ...x, perms: next } : x))
    }
    // §P2-J 可吞：权限位更新是写操作，失败即时 Toast 反馈发起人，非周期读数腿。
    catch (e) {
      showToast('权限更新失败: ' + (e.message || e), 'error')
    }
  }

  // 勾选/取消勾选用户权限
  function togglePerm(u, val) {
    setUserPerms(u, val)
  }

  // 提交重置密码
  function doResetPassword() {
    if (!pwValue) { showToast('密码不能为空', 'warning'); return }
    api.setAdminUserPassword(pwUser.id, pwValue)
      .then(() => { showToast(pwUser.username + ' 密码已重置', 'success'); setPwUser(null) })
      // §P2-J 可吞：重置密码是写操作，失败即时 Toast 反馈发起人，非周期读数腿。
      .catch((e) => showToast('重置失败: ' + (e.message || e), 'error'))
  }

  // 启用/禁用账号
  async function toggleEnabled(u) {
    try {
      await api.setAdminUserEnabled(u.id, !u.enabled)
      setUsers(users.map((x) => x.id === u.id ? { ...x, enabled: !x.enabled } : x))
    }
    // §P2-J 可吞：启停账号是写操作，失败即时 Toast 反馈发起人，非周期读数腿。
    catch (e) {
      showToast('操作失败: ' + (e.message || e), 'error')
    }
  }

  // 打开设置有效期弹窗
  function askSetExpiry(u) {
    const cur = u.expires_at ? Math.ceil((u.expires_at * 1000 - Date.now()) / 86400000) : 0
    setExpUser(u)
    setExpDays(cur > 0 ? cur : 0)
  }

  // 提交设置有效期
  function doSetExpiry() {
    if (isNaN(expDays) || expDays < 0) { showToast('请输入非负整数天数', 'warning'); return }
    const days = expDays || 0
    api.setAdminUserExpiry(expUser.id, days)
      .then(() => {
        // 86400 = 一天的秒数；days 为 0 表示永久，有效期置 0
      setUsers(users.map((x) => x.id === expUser.id ? { ...x, expires_at: days > 0 ? Math.floor(Date.now() / 1000) + days * 86400 : 0 } : x))
        showToast(expUser.username + ' 有效期已更新', 'success')
        setExpUser(null)
      })
      // §P2-J 可吞：设置有效期是写操作，失败即时 Toast 反馈发起人，非周期读数腿。
      .catch((e) => showToast('设置失败: ' + (e.message || e), 'error'))
  }

  // 确认并删除用户
  function askDeleteUser(u) {
    confirmDialog('确定删除用户 ' + u.username + '？该操作不可恢复。', '删除用户').then((ok) => {
      if (!ok) return
      api.deleteAdminUser(u.id)
        .then(() => { setUsers(users.filter((x) => x.id !== u.id)); showToast(u.username + ' 已删除', 'success') })
        // §P2-J 可吞：删除账号是写操作，失败即时 Toast 反馈发起人，非周期读数腿。
        .catch((e) => showToast('删除失败: ' + (e.message || e), 'error'))
    })
  }

  // 打开指定用户的战法参数配置弹窗并加载其专属配置
  // §P2-I 判定：这是「点开弹窗时的一次性读取」，无 setInterval/SSE 周期驱动，不存在轮询自相覆盖，
  // 不接守卫；但它是数据读取腿，读取失败须进台账（区别于下面的写操作）。
  async function openStrategy(u) {
    setActiveUser(u)
    setActiveStrategy({ dragon: {}, double_bump: {}, n_shape: {}, dragon_return: {}, momentum: {} })
    setStrategyMsg('')
    try {
      // 请求该用户在后端的战法参数专属配置
      const sc = await api.fetchAdminStrategyConfig(u.id)
      if (sc) {
        const next = { dragon: {}, double_bump: {}, n_shape: {}, dragon_return: {}, momentum: {} }
        // 按分组把后端返回归并到五大战法占位对象中
        for (const group of strategyGroups) {
          const src = sc[group.key]
          if (src) Object.assign(next[group.key], src)
        }
        setActiveStrategy(next)
      }
      clearLoadFail('战法参数配置') // §P2-J：读取成功即销案
    } catch (e) {
      setStrategyMsg('读取配置失败: ' + (e.message || e)); setStrategyMsgType('err')
      markLoadFail('战法参数配置', (e && e.message) || String(e)) // §P2-J：读取失败点名
    }
  }

  // 关闭战法参数代配弹窗：清空当前选中用户
  function closeStrategy() { setActiveUser(null) }

  // 保存当前选中用户的战法参数下发配置
  async function saveStrategy() {
    if (!activeUser) return
    setStrategySaving(true); setStrategyMsg('')
    try {
      await api.setAdminStrategyConfig(activeUser.id, activeStrategy)
      setStrategyMsg('已保存，该账号热更新即时生效'); setStrategyMsgType('ok')
    }
    // §P2-J 可吞：下发战法参数是写操作，失败内联回弹窗提示（strategyMsg）给发起人，非周期读数腿。
    catch (e) {
      setStrategyMsg('保存失败: ' + (e.message || e)); setStrategyMsgType('err')
    }
    setStrategySaving(false)
  }

  // 用户列表表格列定义：用户信息 / 权限勾选 / 操作按钮组
  const userColumns = [
    {
      // 用户列：展示用户名 + 角色/禁用标签 + ID/创建时间/有效期摘要
      colKey: 'user', title: '用户', width: 220,
      cell: ({ row }) => (
        // 用户列单元格：用户名/角色标签/禁用标记 + ID、创建时间、有效期摘要
        <div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span style={{ fontSize: 15, fontWeight: 600 }}>{row.username}</span>
            <Tag theme={row.role === 'admin' ? 'success' : 'primary'} variant="light">
              {row.role === 'admin' ? '管理员' : '用户'}
            </Tag>
            {/* §MT 租户标签（平台视角下区分归属；租户 admin 全员同租户也统一展示） */}
            {tenantNames[row.tenant_id || 't_default'] && (
              <Tag variant="light" title="所属租户">{tenantNames[row.tenant_id || 't_default']}</Tag>
            )}
            {!row.enabled && <Tag theme="danger" variant="light">已禁用</Tag>}
          </div>
          <div style={{ fontSize: 12, color: 'var(--app-muted)', marginTop: 4 }}>
            ID: {row.id} · 创建于 {fmtTime(row.created_at)} · {expiryText(row)}
          </div>
        </div>
      ),
      // 用户列配置结束
    },
    {
      // 权限列：勾选框组实时更新用户权限位（admin 账号禁用勾选，防止误改）
      colKey: 'perms', title: '权限', width: 220,
      cell: ({ row }) => (
        <Checkbox.Group
          value={uPerms(row)}
          disabled={row.role === 'admin'}
          onChange={(val) => togglePerm(row, val)}
        >
          {allPerms.map((p) => (
            <Checkbox key={p} value={p}>{permLabel(p)}</Checkbox>
          ))}
        </Checkbox.Group>
      ),
    },
    {
      // 操作列：角色切换 / 重置密码 / 启禁用 / 有效期 / 战法代配 / 删除（管理员账号受保护不可操作）
      colKey: 'ops', title: '操作', width: 360,
      cell: ({ row }) => (
        // 操作按钮组：角色升降 / 重置密码 / 启禁用 / 有效期 / 战法代配 / 删除
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
          <Button size="small" theme="default" onClick={() => toggleRole(row)}>
            {row.role === 'admin' ? '降为普通用户' : '设为管理员'}
          </Button>
          <Button size="small" theme="default" onClick={() => { setPwUser(row); setPwValue('') }}>重置密码</Button>
          <Button size="small" theme="default" disabled={row.role === 'admin'} onClick={() => toggleEnabled(row)}>
            {row.enabled ? '禁用' : '启用'}
          </Button>
          <Button size="small" theme="default" disabled={row.role === 'admin'} onClick={() => askSetExpiry(row)}>设置有效期</Button>
          <Button size="small" theme="primary" onClick={() => openStrategy(row)}>配战法参数</Button>
          <Button size="small" theme="danger" disabled={row.role === 'admin'} onClick={() => askDeleteUser(row)}>删除</Button>
        </div>
      ),
      // 操作列配置结束
    },
  ]

  // 挂载时加载用户与权限列表
  useEffect(() => { loadUsers() }, [])

  // ── §DAILY_OPSLOG 运行日志加载 ──
  // 拉日期列表；为空（尚无日志）保持静默。默认选中最新一天。
  // §P2-I：日期清单守卫——它由挂载 effect 与手动刷新 refreshOps 两处驱动，二者可与 30s 正文
  // 轮询的刷新交错触发（快速连点刷新时旧响应迟到会把 opsDates 盖回上一轮），故发起前盖代号、
  // await 回来后先判后到再写 opsDates。
  const loadOpsDates = async () => {
    const token = opsDatesGuard.begin() // 发起任何请求**之前**盖章
    try {
      const res = await api.fetchOpslogDates()
      // §P2-I：后到的旧轮次整包丢弃，一个 state 都不写（连返回给调用方的 ds 也返回空，避免误设 opsDate）
      if (opsDatesGuard.isStale(token)) return []
      // 仅取 dates 字段，缺失时按空数组处理
      const ds = (res && res.dates) || []
      setOpsDates(ds)
      clearLoadFail('日期清单') // §P2-J：本轮成功即销案
      return ds
    } catch (e) {
      // §P2-I：这是本轮自己的请求出错，但若已被更新轮次超越，其失败结论已过时，不写台账
      if (opsDatesGuard.isStale(token)) return []
      showToast('日志日期列表加载失败: ' + (e.message || e), 'error')
      markLoadFail('日期清单', (e && e.message) || String(e)) // §P2-J：读取失败点名
      return []
    }
  }
  // 拉某日内容；date 空串 = 服务端今天
  // §P2-I：正文腿守卫——loadOpslog 被 30s 定时器、opsDate effect、手动刷新三处交错驱动，
  // setInterval 持的是挂载期闭包（loadOpsDates/opsDate 的旧引用），上一轮 fetchOpslog 在途时
  // 下一轮已发出，旧响应迟到会整包覆盖 opsLines/opsMeta（倒挂，显示到下次轮询）。发起前盖章、
  // await 回来后先判后到再写 state。
  const loadOpslog = async (date) => {
    setOpsLoading(true)
    const token = opslogGuard.begin() // 发起任何请求**之前**盖章
    try {
      const res = await api.fetchOpslog(date || '', 2000)
      // §P2-I：后到的旧轮次整包丢弃，日志正文/元信息一个 state 都不写
      if (opslogGuard.isStale(token)) return
      setOpsLines((res && res.lines) || [])
      setOpsMeta({ total: (res && res.total) || 0, truncated: !!(res && res.truncated) })
      clearLoadFail('运行日志') // §P2-J：本轮成功即销案
    } catch (e) {
      // §P2-I：本轮请求自身出错，但若已被更新轮次超越，失败结论已过时（新轮会给更新判定），
      // 不写台账也不重复弹错，交由最新一轮定夺
      if (opslogGuard.isStale(token)) return
      showToast('日志加载失败: ' + (e.message || e), 'error')
      markLoadFail('运行日志', (e && e.message) || String(e)) // §P2-J：读取失败点名
    } finally {
      // 仅非 stale 轮次复位 spinner：stale 轮不写任何 state（连 opsLoading 也不动），
      // 由最新一轮完成时自行复位，避免旧响应把新轮次「加载中」状态盖掉。
      if (!opslogGuard.isStale(token)) setOpsLoading(false)
    }
  }
  // 首次进入：日期列表 → 默认选最新一天并加载
  useEffect(() => {
    ;(async () => {
      const ds = await loadOpsDates()
      if (ds && ds.length) setOpsDate(ds[0].date)
    })()
  }, [])
  // 选中日期变化 → 加载内容 + 滚到底部
  useEffect(() => {
    if (!opsDate) return
    loadOpslog(opsDate)
  }, [opsDate])
  // 内容变化 → 滚到底部（最新事件在文件尾部）
  useEffect(() => {
    if (opsBodyRef.current) opsBodyRef.current.scrollTop = opsBodyRef.current.scrollHeight
  }, [opsLines])
  // 30s 轮询当前日期（盘中排查实时性；日期列表不轮询）
  useEffect(() => {
    const t = setInterval(() => { if (opsDate) loadOpslog(opsDate) }, 30000)
    return () => clearInterval(t)
  }, [opsDate])
  // 手动刷新：日期列表 + 当前内容
  const refreshOps = async () => {
    const ds = await loadOpsDates()
    if (ds && ds.length && !ds.some((d) => d.date === opsDate)) {
      setOpsDate(ds[0].date) // 出现了新的一天 → 切过去（触发 loadOpslog）
    } else {
      loadOpslog(opsDate)
    }
  }

  // 日志行渲染：把 `[quant]`/`[research]` 标签染成不同颜色，其余保持原样
  function renderOpsLine(line, i) {
    const m = line.match(/^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}) \[(quant|research)\] (.*)$/)
    if (!m) return <div key={i} style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>{line}</div>
    const color = m[2] === 'quant' ? '#3b82f6' : '#10b981'
    return (
      <div key={i} style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
        <span style={{ color: '#94a3b8' }}>{m[1]} </span>
        <span style={{ color, fontWeight: 600 }}>[{m[2]}] </span>
        <span>{m[3]}</span>
      </div>
    )
  }

  // 权限守卫：非管理员（api.isAdmin() 返回 false）直接渲染"无权限访问"，
  // 既禁止通过 URL 直接访问，也避免越权渲染管理界面（数据由 loadUsers 守卫拦截）
  if (!isAdmin()) {
    return (
      <div className="page" style={{ textAlign: 'center', paddingTop: 80 }}>
        <h2 style={{ fontSize: 18, fontWeight: 600 }}>无权限访问</h2>
        <p style={{ color: 'var(--app-muted)', fontSize: 13, marginTop: 8 }}>
          该页面仅限管理员访问，请联系管理员或使用管理员账号登录。
        </p>
      </div>
    )
  }

  return (
    <div className="page">
      <h2 style={{ fontSize: 18, fontWeight: 600, marginBottom: 16 }}>用户管理</h2>

      {/* §P2-J（2026-10-06 修复批 波 6）吞错可见化红条：任一读取腿（用户列表/租户列表/
          日期清单/运行日志/战法参数配置）本轮没读到就在此点名。旧形态是各腿失败只弹一次 Toast 或
          什么都不留——界面照常显示上一轮读数或空列表，运维只能靠「今天怎么没数据」反推链路坏了（且方向常错）。
          红条只报「哪条腿失败 + 原因」，**绝不清空既有数据**（把旧读数抹掉只会让「读取失败」更像「今天真的没有」，
          是更坏的可观测性）；下一轮成功即自动销案。
          本条只认共享组件 LoadFailBanner（单实现、单 testid、单文案），不在页内抄一段 div——
          抄一份就等于把「红条在位」派生锁推回「逐页认文案」，加一页漏一页。
          English: §P2-J — per-leg load-failure banner via the single shared component. */}
      <LoadFailBanner fails={loadFails} page="Admin" />

      <Card title="开通新账号" style={{ marginBottom: 12 }}>
        {
          /* 开通新账号表单：录入用户名/初始密码/角色/权限/有效期 */
        }
        <Form layout="vertical">
          <Form.FormItem label="用户名">
            {/* §F31 修复：placeholder 从"登录名"这种含糊提示改为具体格式约束——
                旧版没有 min-length / 字符集提示，用户输"1"或"admin"（与已存在的 admin 撞车）
                提交才被后端拒；现前端预校验。 */}
            <Input value={newUser.username} onChange={(v) => setNewUser({ ...newUser, username: v })}
              placeholder="3-20 位，字母/数字/下划线，首字符为字母" />
          </Form.FormItem>
          <Form.FormItem label="初始密码">
            <Input type="password" value={newUser.password} onChange={(v) => setNewUser({ ...newUser, password: v })}
              placeholder="至少 8 位，首次登录后可自行修改" />
          </Form.FormItem>
          {
            /* 角色选择：普通用户 / 管理员 */
          }
          <Form.FormItem label="角色">
            <Select value={newUser.role} onChange={(v) => setNewUser({ ...newUser, role: v })} style={{ width: 200 }}>
              <Select.Option value="user">普通用户</Select.Option>
              <Select.Option value="admin">管理员</Select.Option>
            </Select>
          </Form.FormItem>
          {
            /* §MT 平台运营者可选目标租户；租户 admin 建号恒落本租户，不显示此项 */
            platform && (
            <Form.FormItem label="租户">
              <Select value={newUser.tenantId} onChange={(v) => setNewUser({ ...newUser, tenantId: v })} style={{ width: 200 }}>
                {tenants.map((t) => (
                  <Select.Option key={t.id} value={t.id}>{t.name}（{t.used_users}/{t.max_users}）</Select.Option>
                ))}
              </Select>
            </Form.FormItem>
            )
          }
          {
            /* 权限勾选：把可用的权限位分配给新账号 */
          }
          <Form.FormItem label="权限">
            <Checkbox.Group value={newUser.perms} onChange={(val) => setNewUser({ ...newUser, perms: val })}>
              {allPerms.map((p) => (
                <Checkbox key={p} value={p}>{permLabel(p)}</Checkbox>
              ))}
            </Checkbox.Group>
          </Form.FormItem>
          {
            /* 有效期设置：天数 + "永久"开关互斥 */
          }
          <Form.FormItem label="有效期">
            <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
              <InputNumber
                value={newUser.expiresDays}
                min={0}
                disabled={newUser.permanent}
                onChange={(v) => setNewUser({ ...newUser, expiresDays: v || 0 })}
                placeholder="天数"
                style={{ width: 140 }}
              />
              {
                /* 永久开关开启后天数输入禁用 */
              }
              <ToggleSw checked={newUser.permanent} onChange={(v) => setNewUser({ ...newUser, permanent: v })} />
              <span style={{ fontSize: 13, color: 'var(--app-muted)' }}>永久</span>
            </div>
            <span style={{ fontSize: 12, color: 'var(--app-muted)' }}>填天数表示到期后自动失效；开启"永久"则不过期</span>
          </Form.FormItem>
        </Form>
        {
          /* 提交创建按钮与结果提示 */
        }
        <Button theme="primary" loading={creating} onClick={createUser}>
          {creating ? '创建中...' : '创建账号'}
        </Button>
        {createMsg && (
          <span style={{ marginLeft: 10, fontSize: 13, color: createMsgType === 'ok' ? 'var(--app-down)' : 'var(--app-up)' }}>{createMsg}</span>
        )}
      </Card>

      <Card title="账号列表" style={{ marginBottom: 12 }}
        // §U-5 卡片右上角：僵尸账号清理入口（先预览后确认，非破坏式两步）
        actions={
          <Button size="small" variant="outline" theme="danger" loading={cleaning} onClick={runUserCleanup}>
            清理失效账号
          </Button>
        }>
        <Table
          rowKey="id"
          data={users}
          columns={userColumns}
          bordered={false}
          size="medium"
          // §FIX-20260902 补 total=长度：不传 total 时 tdesign 分页错显「共 0 条」且无法翻页
          // §D6 修复：受控分页（pageSize 不再因增删/角色变更重渲染被重置）
          pagination={{ ...userPage, total: users.length, showJumper: true,
            onChange: (pi) => setUserPage((p) => ({ ...p, current: pi.current ?? p.current, pageSize: pi.pageSize ?? p.pageSize })) }}
        />
      </Card>

      {/* ── §MT 租户管理（仅平台运营者可见：t_default 之外的租户签发/配额/启停）── */}
      {platform && (
      <Card title="租户管理" style={{ marginBottom: 12 }}
        actions={<Button size="small" variant="outline" onClick={loadTenants}>刷新</Button>}>
        <Table
          rowKey="id"
          data={tenants}
          columns={[
            { colKey: 'name', title: '租户', width: 180, cell: ({ row }) => (
              <div>
                <span style={{ fontWeight: 600 }}>{row.name}</span>
                {row.is_default && <Tag variant="light" style={{ marginLeft: 6 }}>系统</Tag>}
                <div style={{ fontSize: 12, color: 'var(--app-muted)' }}>{row.id}</div>
              </div>
            ) },
            { colKey: 'usage', title: '成员', width: 110, cell: ({ row }) => `${row.used_users}/${row.max_users}` },
            { colKey: 'rate', title: 'API限流/分', width: 110, cell: ({ row }) => row.api_rate_per_min },
            { colKey: 'state', title: '状态', width: 90, cell: ({ row }) => (
              <Tag theme={row.enabled ? 'success' : 'danger'} variant="light">{row.enabled ? '启用' : '停用'}</Tag>
            ) },
            { colKey: 'ops', title: '操作', width: 220, cell: ({ row }) => (
              <div style={{ display: 'flex', gap: 6 }}>
                <Button size="small" theme="default" onClick={() => editTenantQuota(row)}>改配额</Button>
                <Button size="small" theme="default" disabled={row.is_default} onClick={() => toggleTenantEnabled(row)}>
                  {row.enabled ? '停用' : '启用'}
                </Button>
              </div>
            ) },
          ]}
          bordered={false}
          size="medium"
          pagination={false}
        />
        <Form layout="inline" style={{ marginTop: 12, display: 'flex', flexWrap: 'wrap', gap: 8, alignItems: 'flex-end' }}>
          <Form.FormItem label="新租户名">
            <Input value={tenantForm.name} onChange={(v) => setTenantForm({ ...tenantForm, name: v })} placeholder="如：某某私募" style={{ width: 160 }} />
          </Form.FormItem>
          <Form.FormItem label="成员上限">
            <InputNumber value={tenantForm.maxUsers} min={1} onChange={(v) => setTenantForm({ ...tenantForm, maxUsers: v })} style={{ width: 110 }} />
          </Form.FormItem>
          <Form.FormItem label="API/分钟">
            <InputNumber value={tenantForm.apiRate} min={0} onChange={(v) => setTenantForm({ ...tenantForm, apiRate: v })} style={{ width: 110 }} />
          </Form.FormItem>
          <Form.FormItem label="租户管理员(可选)">
            <Input value={tenantForm.adminUser} onChange={(v) => setTenantForm({ ...tenantForm, adminUser: v })} placeholder="用户名" style={{ width: 130 }} />
          </Form.FormItem>
          <Form.FormItem label="初始密码">
            <Input type="password" value={tenantForm.adminPass} onChange={(v) => setTenantForm({ ...tenantForm, adminPass: v })} placeholder="≥8位" style={{ width: 130 }} />
          </Form.FormItem>
          <Button theme="primary" loading={tenantSaving} onClick={createTenant}>创建租户</Button>
        </Form>
      </Card>
      )}

      {/* ── §DAILY_OPSLOG 系统运行日志（每日核心记录，管理员可查）── */}
      <Card
        title="系统运行日志（每日核心记录）"
        style={{ marginBottom: 12 }}
        // §修复：TDesign Card 无 headerRightContent 属性，头部右侧插槽是 actions——
        // 此前误用属性名导致日志日期下拉+刷新按钮从未渲染（静默丢 UI）。
        actions={
          // 卡片右上角操作区：日志日期下拉 + 手动刷新按钮
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            {/* 日期下拉：选择要查看的日志日（YYYYMMDD → YYYY-MM-DD 展示）+ 手动刷新按钮 */}
            <Select value={opsDate} onChange={(v) => setOpsDate(v)} style={{ width: 150 }} size="small" clearable={false}>
              {opsDates.map((d) => (
                <Select.Option key={d.date} value={d.date} label={d.date.replace(/(\d{4})(\d{2})(\d{2})/, '$1-$2-$3')} />
              ))}
            </Select>
            <Button size="small" variant="outline" loading={opsLoading} onClick={refreshOps}>刷新</Button>
          </div>
        }
      >
        {opsDates.length === 0 && !opsLoading ? (
          // 空态：尚无任何日志日期
          <div style={{ fontSize: 13, color: 'var(--app-muted)' }}>
            暂无运行日志（引擎/研究服务启动后自动产出，每天一份，保留 90 天）
          </div>
        ) : (
          // 有日志：概要条 + 终端风格日志正文
          <>
            {/* 概要条：总行数 / 截断提示 / 双引擎（quant=量化、research=研究）图例 */}
            <div style={{ fontSize: 12, color: 'var(--app-muted)', marginBottom: 6 }}>
              共 {opsMeta.total} 行
              {opsMeta.truncated ? '（仅显示最后 2000 行）' : ''}
              {' · '}<span style={{ color: '#3b82f6', fontWeight: 600 }}>quant</span> = 量化引擎
              {' · '}<span style={{ color: '#10b981', fontWeight: 600 }}>research</span> = 研究调度
              {' · '}自动滚动到最新事件，30s 自动刷新
            </div>
            {/* 日志正文：等宽字体终端样式，ref 用于自动滚动到最新事件 */}
            <div
              ref={opsBodyRef}
              style={{
                // 日志正文终端样式：等宽字体、浅底细边框、限高内部滚动
                fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, 'Courier New', monospace",
                fontSize: 12, lineHeight: 1.55,
                background: 'rgba(128,128,128,0.08)',
                border: '1px solid rgba(128,128,128,0.2)',
                borderRadius: 6, padding: '8px 10px',
                maxHeight: 480, overflowY: 'auto',
              }}
            >
              {opsLines.length === 0 && !opsLoading
                // 空记录与日志行列表切换
                ? <div style={{ color: 'var(--app-muted)' }}>该日暂无记录</div>
                : opsLines.map(renderOpsLine)}
            </div>
          </>
        )}
      </Card>

      {/* 重置密码弹窗 */}
      <Dialog
        visible={!!pwUser}
        header={pwUser ? '为 ' + pwUser.username + ' 重置密码' : '重置密码'}
        onClose={() => setPwUser(null)}
        onConfirm={doResetPassword}
        confirmBtn="确定重置"
      >
        <Input type="password" value={pwValue} onChange={(v) => setPwValue(v)} placeholder="输入新密码（留空取消）" />
      </Dialog>

      {/* 设置有效期弹窗 */}
      <Dialog
        visible={!!expUser}
        header={expUser ? '为 ' + expUser.username + ' 设置有效期' : '设置有效期'}
        onClose={() => setExpUser(null)}
        onConfirm={doSetExpiry}
        confirmBtn="确定"
      >
        <div style={{ marginBottom: 8, fontSize: 13, color: 'var(--app-muted)' }}>
          输入天数（0 表示永久）{expUser && expUser.expires_at ? '；当前剩余约 ' + (expUser.expires_at ? Math.ceil((expUser.expires_at * 1000 - Date.now()) / 86400000) : 0) + ' 天' : '；当前永久'}
        </div>
        <InputNumber value={expDays} min={0} onChange={(v) => setExpDays(v || 0)} style={{ width: 200 }} />
      </Dialog>

      {/* 代配战法参数弹窗 */}
      <Dialog
        visible={!!activeUser}
        header={activeUser ? '为 ' + activeUser.username + ' 配置战法参数' : '配置战法参数'}
        onClose={closeStrategy}
        onConfirm={saveStrategy}
        confirmBtn={strategySaving ? '保存中...' : '保存该账号战法参数'}
        width={560}
        footer={strategySaving ? null : undefined}
      >
        {strategyGroups.map((group) => (
          // 遍历五大战法分组，每组渲染一张参数卡片
          <Card key={group.key} title={group.title} style={{ marginBottom: 10 }}>
            {group.fields.map((f) => (
              // 逐字段渲染：按字段类型生成控件
              <Form.FormItem key={f.k} label={f.label} style={{ marginBottom: 8 }}>
                {f.type === 'switch' ? (
                  // 开关型字段：ToggleSw 布尔控件
                  <ToggleSw
                    checked={!!(activeStrategy[group.key] && activeStrategy[group.key][f.k])}
                    onChange={(v) => setActiveStrategy({
                      ...activeStrategy,
                      [group.key]: { ...activeStrategy[group.key], [f.k]: v },
                    })}
                  />
                ) : (
                  // 数字输入型字段：InputNumber，步进取字段配置
                  <InputNumber
                    step={f.step || 'any'}
                    value={(activeStrategy[group.key] && activeStrategy[group.key][f.k]) ?? ''}
                    onChange={(v) => setActiveStrategy({
                      ...activeStrategy,
                      [group.key]: { ...activeStrategy[group.key], [f.k]: v },
                    })}
                    placeholder="0"
                    style={{ width: 200 }}
                  />
                )}
              </Form.FormItem>
            ))}
            // 单个分组卡片渲染结束
          </Card>
        ))}
        {strategyMsg && (
          <span style={{ fontSize: 13, color: strategyMsgType === 'ok' ? 'var(--app-down)' : 'var(--app-up)' }}>{strategyMsg}</span>
        )}
      </Dialog>
    </div>
  )
}
