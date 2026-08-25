<!--
  用户管理页 Admin.vue（仅 admin 可见）
  Admin page Admin.vue (admin only)
  账号开通、角色/权限配置、改密、启禁用、以及为他人账号代配战法参数。
  Create accounts, set role/perms, reset passwords, enable/disable,
  and configure per-account strategy params on behalf of users.

  【核心数据流】挂载即拉取全部账号与系统权限位定义；列表上的角色/权限/启停等操作采用
  「请求成功后就地改本地对象」的乐观更新，失败时提示并回滚；代配战法参数在打开弹层时按
  目标账号拉取配置回填表单，保存后该账号运行中的策略热更新即时生效。
  注：QMT 网关状态展示与实盘下单入口在持仓页的「实盘持仓」tab，不在本页。
  【后端接口】fetchAdminUsers（账号列表 + 权限位定义）；createAdminUser（开户）；
  setAdminUserRole / setAdminUserPerms / setAdminUserPassword / setAdminUserEnabled /
  setAdminUserExpiry（改角色·权限·密码·启禁用·有效期）；deleteAdminUser（删号）；
  fetchAdminStrategyConfig / setAdminStrategyConfig（按账号读写战法参数）。
-->
<template>
  <div class="admin-page">
    <h2>用户管理</h2>

    <!-- 开户表单：填写用户名/密码/角色/权限/有效期，提交后创建新账号 -->
    <div class="card">
      <div class="card-header">开通新账号</div>
      <div class="form-grid">
        <div class="form-row">
          <label>用户名</label>
          <input v-model="newUser.username" placeholder="登录名" />
        </div>
        <div class="form-row">
          <label>初始密码</label>
          <input v-model="newUser.password" placeholder="首次登录用" type="password" />
        </div>
        <div class="form-row">
          <label>角色</label>
          <select v-model="newUser.role">
            <option value="user">普通用户</option>
            <option value="admin">管理员</option>
          </select>
        </div>
        <div class="form-row">
          <label>权限</label>
          <div class="perm-checks">
            <label v-for="p in allPerms" :key="p" class="perm-check">
              <input type="checkbox" :value="p" v-model="newUser.perms" />
              {{ permLabel(p) }}
            </label>
          </div>
        </div>
        <div class="form-row">
          <label>有效期</label>
          <div class="expiry-row">
            <input v-model.number="newUser.expiresDays" type="number" min="0" placeholder="天数" :disabled="newUser.permanent" />
            <label class="perm-check">
              <input type="checkbox" v-model="newUser.permanent" /> 永久
            </label>
          </div>
          <span class="field-hint">填天数表示到期后自动失效；勾选"永久"则不过期</span>
        </div>
      </div>
      <button class="btn-primary" @click="createUser" :disabled="creating">
        {{ creating ? '创建中...' : '创建账号' }}
      </button>
      <span v-if="createMsg" :class="['feedback', createMsgType]">{{ createMsg }}</span>
    </div>

    <!-- 用户列表：每行一个账号，展示角色/启用状态/有效期，并提供改角色、重置密码、启禁用、设有效期、配战法参数、删除等操作 -->
    <div class="card" v-if="users.length">
      <div class="card-header">账号列表</div>
      <div class="user-table">
        <div class="user-row" v-for="u in users" :key="u.id">
          <div class="user-main">
            <div class="user-name">
              {{ u.username }}
              <span :class="['role-tag', u.role === 'admin' ? 'role-admin' : 'role-user']">
                {{ u.role === 'admin' ? '管理员' : '用户' }}
              </span>
              <span v-if="!u.enabled" class="role-tag role-disabled">已禁用</span>
            </div>
            <div class="user-meta">ID: {{ u.id }} · 创建于 {{ fmtTime(u.created_at) }} · {{ expiryText(u) }}</div>
            <div class="perm-checks">
              <label v-for="p in allPerms" :key="p" class="perm-check">
                <input type="checkbox"
                       :checked="uPerms(u).includes(p)"
                       :disabled="u.role === 'admin'"
                       @change="togglePerm(u, p, $event)" />
                {{ permLabel(p) }}
              </label>
            </div>
          </div>
          <div class="user-actions">
            <button class="btn-mini" @click="toggleRole(u)">
              {{ u.role === 'admin' ? '降为普通用户' : '设为管理员' }}
            </button>
            <button class="btn-mini" @click="askResetPassword(u)">重置密码</button>
            <button class="btn-mini" :disabled="u.role === 'admin'" @click="toggleEnabled(u)">
              {{ u.enabled ? '禁用' : '启用' }}
            </button>
            <button class="btn-mini" :disabled="u.role === 'admin'" @click="askSetExpiry(u)">设置有效期</button>
            <button class="btn-mini" @click="openStrategy(u)">配战法参数</button>
            <button class="btn-mini btn-danger" :disabled="u.role === 'admin'" @click="askDeleteUser(u)">删除</button>
          </div>
        </div>
      </div>
    </div>

    <!-- 代配战法参数弹层：管理员代替目标账号编辑各战法（龙头/双响炮/N形/龙回头/动量）的参数并保存，保存后该账号热更新即时生效 -->
    <div v-if="activeUser" class="modal-mask" @click.self="closeStrategy">
      <div class="modal">
        <div class="modal-header">
          <span>为 {{ activeUser.username }} 配置战法参数</span>
          <button class="btn-close" @click="closeStrategy">✕</button>
        </div>
        <div class="strategy-groups">
          <div v-for="group in strategyGroups" :key="group.key" class="card group-card">
            <div class="card-header">{{ group.title }}</div>
            <div class="form-row" v-for="f in group.fields" :key="f.k">
              <label :title="f.hint || ''">{{ f.label }}</label>
              <label v-if="f.type === 'switch'" class="switch">
                <input type="checkbox" v-model="activeStrategy[group.key][f.k]" />
                <span class="slider"></span>
              </label>
              <input v-else
                     v-model.number="activeStrategy[group.key][f.k]"
                     :type="f.type || 'number'"
                     :step="f.type === 'number' ? (f.step || 'any') : undefined"
                     placeholder="0" />
            </div>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn-primary" @click="saveStrategy" :disabled="strategySaving">
            {{ strategySaving ? '保存中...' : '保存该账号战法参数' }}
          </button>
          <span v-if="strategyMsg" :class="['feedback', strategyMsgType]">{{ strategyMsg }}</span>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
// ── 依赖导入 ──
// Vue 组合式 API：ref 响应式状态 + onMounted 生命周期钩子
import { ref, onMounted } from 'vue'
// 后端管理接口封装：用户 CRUD、角色/权限/密码/有效期设置、按账号代配战法参数
import * as api from '../api/index.js'

// ── 状态 ──
// 全部账号列表（含 id/角色/权限/启用状态/有效期），用于账号列表区渲染
const users = ref([])
// 系统支持的权限位定义（如 research_approve 研究审批），由后端返回
const allPerms = ref([])
// 开户请求进行中标记，用于禁用创建按钮防重复提交
const creating = ref(false)
// 开户表单的结果提示文案
const createMsg = ref('')
// 开户提示的类型：ok=成功 / err=失败，决定提示文字颜色
const createMsgType = ref('ok')
// 开户表单模型：用户名、初始密码、角色、权限位、有效期天数、是否永久
const newUser = ref({ username: '', password: '', role: 'user', perms: [], expiresDays: 0, permanent: true })

// 当前正在为其代配战法参数的用户（非 null 时弹层显示），null 表示弹层关闭
const activeUser = ref(null)
// 弹层内编辑的战法参数，结构为 { 分组key: { 字段key: 值 } }，保存后整体提交
const activeStrategy = ref({})
// 战法参数保存中标记，用于禁用保存按钮
const strategySaving = ref(false)
// 战法参数操作的结果提示文案
const strategyMsg = ref('')
// 战法参数提示类型：ok=成功 / err=失败
const strategyMsgType = ref('ok')

// 战法参数分组定义（与 Settings.vue 一致）
// 该数组驱动弹层表单的 v-for 渲染：key 即参数对象的命名空间，fields 声明各字段的标签/步长/
// 控件类型（type=switch 渲染为开关，其余为数字输入）；新增战法或字段只需在此追加，无需改模板。
const strategyGroups = [
  {
    key: 'dragon', title: '龙头战法（权重合计≤1）',
    fields: [
      { k: 'f1_seal_weight', label: 'F1 首封权重', step: 0.05 },
      { k: 'f2_resonance_weight', label: 'F2 共振权重', step: 0.05 },
      { k: 'f3_premium_weight', label: 'F3 溢价权重', step: 0.05 },
      { k: 'f4_rs_weight', label: 'F4 强度权重', step: 0.05 },
      { k: 'pullback_max_pct', label: '最大回撤%', step: 0.01 },
      { k: 'breaker_sell_half_pct', label: '炸板减半%', step: 0.01 },
      { k: 'breaker_sell_all_pct', label: '炸板清仓%', step: 0.01 },
      { k: 'buy_pullback_sell_half_pct', label: '买入回撤减半%', step: 0.01 },
      { k: 'buy_pullback_sell_all_pct', label: '买入回撤清仓%', step: 0.01 },
      { k: 'buy_day_close_below', label: '买入日收盘低于%', step: 0.01 },
      { k: 'next_open_if_below', label: '次日开盘低于%', step: 0.01 },
      { k: 'take_profit_pct', label: '止盈%', step: 1 },
    ],
  },
  {
    key: 'double_bump', title: '双响炮战法',
    fields: [
      { k: 'first_break_volume_multiple', label: '一突量比', step: 0.1 },
      { k: 'second_break_volume_multiple', label: '二突量比', step: 0.1 },
      { k: 'adjust_vol_ratio_max', label: '调整量比上限', step: 0.5 },
      { k: 'position_weight', label: '调整深度权重', step: 0.05 },
      { k: 'ma_weight', label: '均线权重', step: 0.05 },
      { k: 'volume_weight', label: '量能权重', step: 0.05 },
      { k: 'double_bump_take_profit_pct', label: '止盈%', step: 0.01 },
    ],
  },
  {
    key: 'n_shape', title: 'N 形战法',
    fields: [
      { k: 'n_pattern_score_threshold', label: 'N 形态分阈值', step: 1 },
      { k: 'hard_stop_loss', label: '硬止损%', step: 0.01 },
    ],
  },
  {
    key: 'dragon_return', title: '龙回头战法',
    fields: [
      { k: 'stop_loss_pct', label: '止损%', step: 0.01 },
      { k: 'take_profit_pct', label: '止盈%', step: 0.01 },
      { k: 'max_hold_days', label: '最长持仓天数', step: 1 },
      { k: 'target1_multiplier', label: '目标1倍数', step: 0.05 },
      { k: 'target2_multiplier', label: '目标2倍数', step: 0.05 },
      { k: 'trailing_drawback', label: '移动止损回撤%', step: 0.01 },
    ],
  },
  {
    key: 'momentum', title: '动量分权重（合计建议=100）',
    fields: [
      { k: 'volume_price_weight', label: '量价权重', step: 5 },
      { k: 'macd_weight', label: 'MACD权重', step: 5 },
      { k: 'trend_weight', label: '走势权重', step: 5 },
      { k: 'momentum_gate_enabled', label: '动量提升才提醒', type: 'switch', hint: '开启后仅当动量分提升(或回落≤容忍差)才放行 双响炮/龙头/龙回头 战法信号；N形不受影响' },
      { k: 'momentum_delta_tol', label: '回落容忍差(分)', step: 1, hint: '动量分相对上一轮回落 ≤ 该值仍视为提升；设为0表示需严格不回落' },
    ],
  },
]

/** 权限位显示名 */
function permLabel(p) {
  const m = { research_approve: '研究审批' }
  return m[p] || p
}

/** 时间戳格式化 */
function fmtTime(ts) {
  if (!ts) return '-'
  const d = new Date(ts * 1000)
  return d.getFullYear() + '-' + String(d.getMonth() + 1).padStart(2, '0') + '-' + String(d.getDate()).padStart(2, '0')
}

/** 有效期展示文本：永久 / 到期日(+剩余天数) / 已到期 */
function expiryText(u) {
  if (!u.expires_at) return '有效期：永久'
  const now = Date.now()
  const exp = u.expires_at * 1000
  if (exp < now) return '有效期：已到期'
  const days = Math.ceil((exp - now) / 86400000)
  return '有效期：' + fmtTime(u.expires_at) + '（剩 ' + days + ' 天）'
}

/** 设置有效期：prompt 输入天数，0=永久 */
function askSetExpiry(u) {
  const cur = u.expires_at ? Math.ceil((u.expires_at * 1000 - Date.now()) / 86400000) : 0
  const input = prompt(
    '为 ' + u.username + ' 设置有效期天数（输入 0 表示永久；当前' + (u.expires_at ? '剩余约 ' + (cur > 0 ? cur : '已到期') + ' 天' : '永久') + '）：',
    String(cur > 0 ? cur : 0)
  )
  if (input === null) return
  const days = parseInt(input, 10)
  if (isNaN(days) || days < 0) {
    alert('请输入非负整数天数')
    return
  }
  api.setAdminUserExpiry(u.id, days)
    .then(() => {
      u.expires_at = days > 0 ? Math.floor(Date.now() / 1000) + days * 86400 : 0
      alert(u.username + ' 有效期已更新')
    })
    .catch(e => alert('设置失败: ' + (e.message || e)))
}

/** 用户权限位列表 */
function uPerms(u) {
  return Array.isArray(u.perms) ? u.perms : []
}

/** 加载用户列表 */
async function loadUsers() {
  try {
    const res = await api.fetchAdminUsers()
    users.value = res.users || []
    allPerms.value = res.perms || []
  } catch (e) {
    alert('加载用户失败: ' + (e.message || e))
  }
}

/** 创建账号 */
async function createUser() {
  if (!newUser.value.username || !newUser.value.password) {
    createMsg.value = '用户名和密码必填'
    createMsgType.value = 'err'
    return
  }
  creating.value = true
  createMsg.value = ''
  try {
    await api.createAdminUser({
      username: newUser.value.username,
      password: newUser.value.password,
      role: newUser.value.role,
      perms: newUser.value.perms,
      expires_days: newUser.value.permanent ? 0 : (newUser.value.expiresDays || 0),
    })
    createMsg.value = '账号已创建'
    createMsgType.value = 'ok'
    newUser.value = { username: '', password: '', role: 'user', perms: [], expiresDays: 0, permanent: true }
    loadUsers()
  } catch (e) {
    createMsg.value = '创建失败: ' + (e.message || e)
    createMsgType.value = 'err'
  }
  creating.value = false
}

/** 切换角色 */
async function toggleRole(u) {
  const role = u.role === 'admin' ? 'user' : 'admin'
  try {
    await api.setAdminUserRole(u.id, role)
    u.role = role
  } catch (e) {
    alert('操作失败: ' + (e.message || e))
  }
}

/** 切换权限位 */
async function togglePerm(u, p, ev) {
  const next = ev.target.checked
    ? [...uPerms(u), p]
    : uPerms(u).filter(x => x !== p)
  try {
    await api.setAdminUserPerms(u.id, next)
    u.perms = next
  } catch (e) {
    alert('权限更新失败: ' + (e.message || e))
    ev.target.checked = !ev.target.checked
  }
}

/** 重置密码 */
function askResetPassword(u) {
  const pw = prompt('为 ' + u.username + ' 设置新密码（留空取消）')
  if (!pw) return
  api.setAdminUserPassword(u.id, pw)
    .then(() => alert(u.username + ' 密码已重置'))
    .catch(e => alert('重置失败: ' + (e.message || e)))
}

/** 启用/禁用 */
async function toggleEnabled(u) {
  try {
    await api.setAdminUserEnabled(u.id, !u.enabled)
    u.enabled = !u.enabled
  } catch (e) {
    alert('操作失败: ' + (e.message || e))
  }
}

/** 删除用户（二次确认） */
function askDeleteUser(u) {
  if (!confirm('确定删除用户 ' + u.username + '？该操作不可恢复。')) return
  api.deleteAdminUser(u.id)
    .then(() => {
      users.value = users.value.filter(x => x.id !== u.id)
      alert(u.username + ' 已删除')
    })
    .catch(e => alert('删除失败: ' + (e.message || e)))
}

/** 打开战法参数配置弹层 */
async function openStrategy(u) {
  activeUser.value = u
  activeStrategy.value = { dragon: {}, double_bump: {}, n_shape: {}, dragon_return: {}, momentum: {} }
  strategyMsg.value = ''
  try {
    const sc = await api.fetchAdminStrategyConfig(u.id)
    if (sc) {
      for (const group of strategyGroups) {
        const src = sc[group.key]
        if (src) Object.assign(activeStrategy.value[group.key], src)
      }
    }
  } catch (e) {
    strategyMsg.value = '读取配置失败: ' + (e.message || e)
    strategyMsgType.value = 'err'
  }
}

/** 关闭战法参数弹层：清空当前目标用户，模板中的 v-if 使弹层隐藏 */
function closeStrategy() {
  activeUser.value = null
}

/** 保存代配战法参数 */
async function saveStrategy() {
  strategySaving.value = true
  strategyMsg.value = ''
  try {
    await api.setAdminStrategyConfig(activeUser.value.id, activeStrategy.value)
    strategyMsg.value = '已保存，该账号热更新即时生效'
    strategyMsgType.value = 'ok'
  } catch (e) {
    strategyMsg.value = '保存失败: ' + (e.message || e)
    strategyMsgType.value = 'err'
  }
  strategySaving.value = false
}

// 页面挂载后立即拉取一次用户列表与权限位定义
onMounted(loadUsers)
</script>

<style scoped>
/* ── 页面骨架与卡片表单 ── */
.admin-page { max-width: 900px; }
.admin-page h2 { font-size: 18px; font-weight: 600; margin-bottom: 16px; }
.card { background: #1a1a2e; border-radius: 8px; padding: 16px; margin-bottom: 12px; }
.card-header { font-size: 14px; font-weight: 600; color: #ccc; margin-bottom: 12px; }
.form-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 10px 16px; margin-bottom: 12px; }
.form-row { display: flex; flex-direction: column; gap: 4px; font-size: 13px; }
.form-row label { color: #888; }
.form-row input, .form-row select {
  padding: 6px 10px; border-radius: 4px; border: 1px solid #333;
  background: #0f0f23; color: #e0e0e0; font-size: 14px; outline: none;
}
.form-row input:focus, .form-row select:focus { border-color: #FF4D4F; }
.perm-checks { display: flex; flex-wrap: wrap; gap: 6px 14px; padding: 4px 0; }
.perm-check { display: inline-flex; align-items: center; gap: 4px; font-size: 13px; color: #aaa; cursor: pointer; }
.perm-check input { accent-color: #FF4D4F; }
.expiry-row { display: flex; align-items: center; gap: 12px; }
.expiry-row input[type="number"] {
  width: 90px; padding: 6px 10px; border-radius: 4px; border: 1px solid #333;
  background: #0f0f23; color: #e0e0e0; font-size: 14px; outline: none;
}
.field-hint { font-size: 12px; color: #666; }

/* ── 主按钮 ── */
.btn-primary {
  padding: 6px 16px; border-radius: 4px; border: 1px solid #FF4D4F;
  background: transparent; color: #FF4D4F; cursor: pointer; font-size: 14px;
}
.btn-primary:hover { background: rgba(255,77,79,0.1); }
.btn-primary:disabled { opacity: 0.5; cursor: not-allowed; }

/* ── 用户列表（行式卡片：左侧账号信息 + 右侧操作按钮组）── */
.user-table { display: flex; flex-direction: column; gap: 10px; }
.user-row {
  display: flex; align-items: center; justify-content: space-between; gap: 12px;
  padding: 10px 12px; border: 1px solid #2a2a3e; border-radius: 6px;
}
.user-main { flex: 1; min-width: 0; }
.user-name { font-size: 15px; font-weight: 600; color: #e0e0e0; display: flex; align-items: center; gap: 8px; }
.role-tag { font-size: 12px; padding: 1px 8px; border-radius: 4px; }
.role-admin { background: rgba(76,175,80,0.15); color: #4caf50; }
.role-user { background: rgba(100,181,246,0.12); color: #64b5f6; }
.role-disabled { background: rgba(255,77,79,0.15); color: #FF4D4F; }
.user-meta { font-size: 12px; color: #666; margin: 4px 0 6px; }
.user-actions { display: flex; flex-wrap: wrap; gap: 6px; }
.btn-mini {
  padding: 4px 10px; border-radius: 4px; border: 1px solid #333;
  background: transparent; color: #bbb; cursor: pointer; font-size: 12px;
}
.btn-mini:hover { background: #2a2a3e; color: #e0e0e0; }
.btn-mini:disabled { opacity: 0.4; cursor: not-allowed; }
.btn-mini.btn-danger { border-color: #6b2b2b; color: #ff7a7a; }
.btn-mini.btn-danger:hover { background: rgba(255,77,79,0.15); color: #ff9a9a; }
.btn-mini.btn-danger:disabled { opacity: 0.4; }

/* ── 操作结果反馈文案（ok 绿 / err 红）── */
.feedback { font-size: 13px; margin-left: 10px; }
.feedback.ok { color: #4caf50; }
.feedback.err { color: #FF4D4F; }

/* ── 代配战法参数弹层 ── */
.modal-mask {
  position: fixed; inset: 0; background: rgba(0,0,0,0.6);
  display: flex; align-items: center; justify-content: center; z-index: 100;
}
.modal {
  background: #14142a; border-radius: 10px; padding: 18px; width: 92%; max-width: 560px;
  max-height: 86vh; overflow-y: auto; border: 1px solid #2a2a3e;
}
.modal-header { display: flex; align-items: center; justify-content: space-between; font-size: 15px; font-weight: 600; color: #e0e0e0; margin-bottom: 12px; }
.btn-close { background: none; border: none; color: #888; font-size: 16px; cursor: pointer; }
.strategy-groups { display: flex; flex-direction: column; gap: 10px; }
.group-card { padding: 12px; }
.modal-footer { margin-top: 14px; display: flex; align-items: center; gap: 10px; }

/* ── iOS 风格开关（布尔型战法参数，如动量闸门）── */
.switch { position: relative; display: inline-block; width: 44px; height: 24px; }
.switch input { opacity: 0; width: 0; height: 0; }
.slider {
  position: absolute; cursor: pointer; inset: 0; border-radius: 24px;
  background: #333; transition: 0.3s;
}
.slider::before {
  content: ''; position: absolute; height: 18px; width: 18px; left: 3px; top: 3px;
  border-radius: 50%; background: #888; transition: 0.3s;
}
.switch input:checked + .slider { background: #FF4D4F; }
.switch input:checked + .slider::before { transform: translateX(20px); background: #fff; }

/* ── 移动端适配 ── */
@media (max-width: 768px) {
  .form-grid { grid-template-columns: 1fr; }
  .user-row { flex-direction: column; align-items: flex-start; }
}
</style>
