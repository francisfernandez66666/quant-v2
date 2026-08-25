<!--
  模拟盘页面 Paper.vue
  Paper-trading page Paper.vue
  独立于真实持仓的纸面交易：admin 账户的 buy 信号按实时价自动撮合成虚拟持仓；普通用户为手动
  记账（信号页/持仓页手动买入/加仓/减仓，输入价格与手数，静态存储展示）。页内展示分仓余量、
  持仓与成交日志（div-grid + 分时展开 + 移动端底部操作菜单）、净值曲线与信号质量统计。
  Isolated from the real book: on the admin account, strategy buy signals auto-fill at the live price into
  virtual positions; normal users keep a manual book (buy/add/trim typed price and lots, static storage &
  view). The page shows the strategy-pool allocation, positions & fill log (div-grid + K-line expand +
  mobile bottom action sheet), an equity curve and signal-quality stats.
-->
<template>
  <div class="paper-page">
    <!-- 页头：标题 + 清盘重置（Header: title + reset button）-->
    <div class="page-header">
      <h2>模拟盘</h2>
      <div class="header-right">
        <span class="admin-badge" v-if="isAdmin" title="admin 账户的模拟盘支持回测与自动化交易联动">联动版</span>
        <span class="enabled-badge" :class="enabled ? 'on' : 'off'">
          {{ enabled ? (isAdmin ? '自动撮合中' : '手动记账（静态）') : '未启用（rules.paper.enabled）' }}
        </span>
        <span class="cap-badge" v-if="enabled" title="当前生效的持仓上限（经确认资金固化）">
          上限：{{ appliedMax > 0 ? appliedMax + ' 只' : '不设限' }}
        </span>
        <!-- §UI 重构：操作区收敛为三颗主按钮 + 一个统一设置弹窗 -->
        <button class="btn-confirm" :disabled="!enabled" @click="showDepositModal = true"
                title="向模拟盘增量注入资金">＋ 注入资金</button>
        <button class="btn-config" :disabled="!enabled" @click="openSettingsModal"
                title="资金分配 / 仓位上限 / 恢复均分">⚙ 设置</button>
        <button class="btn-reset" :disabled="!enabled" @click="showResetModal = true"
                title="清盘重置：平仓全部持仓并重置净值">清盘</button>
      </div>
    </div>

    <!-- ── 注入资金弹窗 ── -->
    <div class="modal-overlay" v-if="showDepositModal" @click.self="showDepositModal = false">
      <div class="modal">
        <div class="modal-title">注入资金</div>
        <div class="form-row">
          <label>金额（元）</label>
          <input v-model.number="depositAmount" type="number" min="0" step="1000" placeholder="10000" />
        </div>
        <div class="modal-actions">
          <button class="btn-cancel" @click="showDepositModal = false">取消</button>
          <button class="btn-confirm" @click="confirmDeposit(); showDepositModal = false">确认注入</button>
        </div>
      </div>
    </div>

    <!-- ── 清盘弹窗（显式指定重置后初始资金，解决 Deposit 污染 InitialCapital 的 bug）── -->
    <div class="modal-overlay" v-if="showResetModal" @click.self="showResetModal = false">
      <div class="modal">
        <div class="modal-title">⚠ 清盘重置</div>
        <div class="config-hint" style="color:#e6a23c">
          将平仓全部持仓、清除成交日志与净值曲线。
        </div>
        <div class="form-row">
          <label>重置后初始资金</label>
          <input v-model.number="resetToCapital" type="number" min="0" step="10000"
                 placeholder="默认 100000" />
          <span class="static-val">元</span>
        </div>
        <div class="config-hint">不填则按当前累计投入总额重置。</div>
        <div class="form-row">
          <label>持仓上限</label>
          <input v-model.number="resetMaxPos" type="number" min="0" step="1" placeholder="0=不设限" />
        </div>
        <div class="modal-actions">
          <button class="btn-cancel" @click="showResetModal = false">取消</button>
          <button class="btn-reset" @click="doResetV2">确认清盘</button>
        </div>
      </div>
    </div>

    <!-- ── 统一设置弹窗（Tab：资金分配 | 仓位上限）── -->
    <div class="modal-overlay" v-if="settingsOpen" @click.self="settingsOpen = false">
      <div class="modal pool-config-modal">
        <div class="modal-title">⚙ 设置</div>
        <div class="settings-tabs">
          <button :class="['tab', settingsTab === 'alloc' ? 'active' : '']" @click="settingsTab = 'alloc'">资金分配</button>
          <button :class="['tab', settingsTab === 'caps' ? 'active' : '']" @click="settingsTab = 'caps'">仓位上限</button>
          <button :class="['tab', settingsTab === 'rules' ? 'active' : '']" @click="settingsTab = 'rules'">买入纪律</button>
        </div>
        <!-- 资金分配 tab -->
        <div v-show="settingsTab === 'alloc'">
          <div class="config-hint">每池资金额（Σ ≈ 总现金守恒）。不影响仓位上限。</div>
          <div v-for="p in pools" :key="'sa-' + p.key" class="pool-config-row">
            <span class="pool-config-label">{{ p.label }}</span>
            <input v-model.number="cfgAllocs[p.key]" type="number" min="0" step="1000" class="cfg-input"
                   :placeholder="'当前 ¥' + fmt(p.cash)" />
          </div>
        </div>
        <!-- 仓位上限 tab -->
        <div v-show="settingsTab === 'caps'">
          <div class="form-row">
            <label>全局持仓上限</label>
            <input v-model.number="cfgMaxPos" type="number" min="0" step="1" placeholder="0=不设限" />
            <span class="static-val">（0=不设限）</span>
          </div>
          <div class="config-hint">每池持仓上限（0=不单独设限）。Σ ≤ 全局。不影响资金分配。</div>
          <div v-for="p in pools" :key="'sc-' + p.key" class="pool-config-row">
            <span class="pool-config-label">{{ p.label }}</span>
            <input v-model.number="cfgCaps[p.key]" type="number" min="0" step="1" class="cfg-input cfg-cap"
                   :placeholder="p.max_pos > 0 ? '当前 ' + p.max_pos : '不单独设限'" />
          </div>
        </div>
        <!-- 买入纪律 tab（§A3）：下拉选池 → 只渲染该池四字段（战法越挖越多也不撑爆弹窗） -->
        <div v-show="settingsTab === 'rules'">
          <div class="config-hint">
            每池买入纪律：日限次数 / 冷却分钟 / 最低评分 / 日预算%。全 0 = 不设限；
            寻优审批会自动把门槛写入对应池的「最低评分」。
          </div>
          <div class="pool-rules-row">
            <select v-model="cfgRuleSel" class="cfg-select">
              <option v-for="p in pools" :key="'sel-' + p.key" :value="p.key">{{ p.label }}</option>
            </select>
            <template v-if="cfgRules[cfgRuleSel]">
              <div class="pool-rules-head">{{ poolLabel(cfgRuleSel) }}<span class="rules-now"
                    v-if="poolCurrentRule(cfgRuleSel)">（当前生效：{{ poolCurrentRuleText(cfgRuleSel) }}）</span></div>
              <div class="pool-rules-grid">
                <label>日限买<input v-model.number="cfgRules[cfgRuleSel].max_daily_buys" type="number" min="0" step="1" placeholder="0=不限" /></label>
                <label>冷却(分)<input v-model.number="cfgRules[cfgRuleSel].cooldown_minutes" type="number" min="0" step="5" placeholder="0=不限" /></label>
                <label>最低分<input v-model.number="cfgRules[cfgRuleSel].min_score" type="number" min="0" max="100" step="1" placeholder="0=不过滤" /></label>
                <label>日预算%<input v-model.number="cfgRules[cfgRuleSel].budget_pct_per_day" type="number" min="0" max="100" step="5" placeholder="0=不限" /></label>
              </div>
            </template>
          </div>
        </div>
        <div class="preview" v-if="cfgWarn">{{ cfgWarn }}</div>
        <div class="modal-actions">
          <button class="btn-cancel" @click="settingsOpen = false">取消</button>
          <button class="btn-confirm" @click="saveSettings">保存</button>
        </div>
      </div>
    </div>


    <!-- 分仓条：当前启用战法的资金池（可点按筛选持仓/成交，展示各池累计涨跌幅）
         (Strategy-pool allocation strip — clickable tabs that filter positions/fills and show each
         pool's cumulative return since buy) -->
    <div class="pools-bar" v-if="enabled && pools.length">
      <div class="pools-title">分仓资金池</div>
      <div class="pool-chip" :class="{ active: activePool === null }" @click="activePool = null">
        <span class="pool-label">全部</span>
        <span class="pool-meta">{{ positions.length }} 仓</span>
      </div>
      <div class="pool-chip" v-for="p in pools" :key="p.key"
           :class="{ active: activePool === (p.key || '__other__'), other: !p.key }"
           :title="p.key || '其他/手动'" @click="togglePool(p.key)">
        <span class="pool-label">{{ p.label }}</span>
        <span class="pool-return" :class="pnlCls(p.return_pct)">
          {{ p.return_pct >= 0 ? '+' : '' }}{{ p.return_pct.toFixed(2) }}%
        </span>
        <span class="pool-cash">¥{{ fmt(p.cash) }}</span>
        <span class="pool-meta">{{ p.ratio_pct.toFixed(1) }}% · {{ p.positions }} 仓</span>
      </div>
      <!-- 单池清盘：仅当选中某分仓时出现（只清该池，不影响其他池/全局净值）-->
      <button v-if="activePool !== null" class="btn-pool-reset"
              :disabled="!enabled" title="只清当前选中池的持仓与累计表现，不影响其他池与全局净值/成交"
              @click="confirmPoolReset">清盘本池</button>
    </div>

    <!-- 绩效统计卡（Performance stat cards；跟随当前分仓 tab 筛选）-->
    <div class="stats-scope" v-if="enabled && activePool !== null">
      <span class="stats-scope-tag">统计范围：{{ activePoolLabel }}</span>
    </div>
    <div class="stats-grid" v-if="activeStats">
      <div class="stat-card">
        <div class="stat-label">总资产</div>
        <div class="stat-value">¥{{ fmt(activeStats.total_value) }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">总收益</div>
        <div class="stat-value" :class="activeStats.total_return_pct >= 0 ? 'up' : 'down'">
          {{ activeStats.total_return_pct >= 0 ? '+' : '' }}{{ activeStats.total_return_pct.toFixed(2) }}%
          <!-- 标注收益计算基数：基于累计投入（注入资金会同步累加），避免"收益百分比失真"的误读 -->
          <!-- Notes the return basis: computed against the cumulative investment (deposits accumulate), so the % reads clearly -->
          <em class="sub">基于累计投入 ¥{{ fmt(activeStats.initial_capital) }}</em>
        </div>
      </div>
      <div class="stat-card">
        <div class="stat-label">当日收益</div>
        <div class="stat-value" :class="activeStats.today_return_pct >= 0 ? 'up' : 'down'">
          {{ activeStats.today_return_pct >= 0 ? '+' : '' }}{{ activeStats.today_return_pct.toFixed(2) }}%
        </div>
      </div>
      <div class="stat-card">
        <div class="stat-label">现金</div>
        <div class="stat-value">¥{{ fmt(activeStats.cash) }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">持仓市值 / 已实现盈亏</div>
        <div class="stat-value">
          ¥{{ fmt(activeStats.market_value) }}
          <em class="sub" :class="activeStats.realized_pnl >= 0 ? 'up' : 'down'">
            {{ activeStats.realized_pnl >= 0 ? '+' : '' }}¥{{ fmt(activeStats.realized_pnl) }}
          </em>
        </div>
      </div>
      <div class="stat-card">
        <div class="stat-label">已平仓胜率</div>
        <div class="stat-value">{{ activeStats.win_rate_pct.toFixed(0) }}% <em class="sub">/ {{ activeStats.open_positions }}仓</em></div>
      </div>
    </div>

    <!-- 信号质量统计卡：仅联动版（admin 自动撮合）有意义（Signal-quality stats, meaningful only on the
         auto-filled admin book）-->
    <div class="stats-grid quality" v-if="activeStats && isAdmin">
      <div class="stat-card">
        <div class="stat-label">已撮合买入信号</div>
        <div class="stat-value">{{ activeStats.filled_buys }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">平均成交延迟</div>
        <div class="stat-value">{{ activeStats.avg_latency_sec }}s <em class="sub">最大 {{ activeStats.max_latency_sec }}s</em></div>
      </div>
      <div class="stat-card">
        <div class="stat-label">平均滑点（成交 vs 信号价）</div>
        <div class="stat-value" :class="activeStats.avg_slippage_pct >= 0 ? 'down' : 'up'">
          {{ activeStats.avg_slippage_pct >= 0 ? '+' : '' }}{{ activeStats.avg_slippage_pct.toFixed(2) }}%
        </div>
      </div>
      <div class="stat-card">
        <div class="stat-label">滑点累计成本</div>
        <div class="stat-value" :class="activeStats.slippage_cost >= 0 ? 'down' : 'up'">
          {{ activeStats.slippage_cost >= 0 ? '+' : '' }}¥{{ fmt(activeStats.slippage_cost) }}
          <em class="sub">占初始 {{ activeStats.signal_amount_pct.toFixed(2) }}%</em>
        </div>
      </div>
    </div>

    <!-- 净值曲线（Equity curve；普通用户为静态记账，无自动净值，显示说明）-->
    <div class="panel" v-if="isAdmin">
      <div class="panel-title">净值曲线 <em class="sub">（{{ stats?.equity_curve_points || 0 }} 个交易日）</em></div>
      <svg v-if="equity.length > 1" class="equity-chart" :viewBox="'0 0 ' + W + ' ' + H" preserveAspectRatio="none">
        <polyline :points="linePoints" fill="none" stroke="#FF4D4F" stroke-width="2" />
        <line v-for="lvl in gridLines" :key="lvl.y" :x1="0" :y1="lvl.y" :x2="W" :y2="lvl.y" class="grid-line" />
      </svg>
      <div v-else class="empty-hint">净值数据不足（自动撮合开启并产生成交后显示）</div>
    </div>

    <!-- 持仓 / 成交日志 页签（tabs: positions / trade log）-->
    <div class="tabs">
      <button class="tab" :class="{ active: tab === 'positions' }" @click="tab = 'positions'">
        当前持仓 <em class="sub">{{ filteredPositions.length }} 只</em>
      </button>
      <button class="tab" :class="{ active: tab === 'trades' }" @click="tab = 'trades'">
        成交日志 <em class="sub">{{ filteredTrades.length }} 笔 · 近3月</em>
      </button>
      <button class="tab" :class="{ active: tab === 'orders' }" @click="tab = 'orders'">
        订单 <em class="sub">{{ filteredOrders.length }} 笔</em>
      </button>
    </div>

    <!-- 持仓列表：div-grid（照搬真实持仓页模式：行内字段 + 分时展开 + 移动端 sheet）-->
    <div class="panel" v-if="tab === 'positions'">
      <div class="panel-title">当前持仓 <em class="sub">{{ filteredPositions.length }} 只</em></div>
      <div class="positions-table" v-if="filteredPositions.length">
        <div class="table-header">
          <span class="col-code">代码</span>
          <span class="col-name">名称</span>
          <span class="col-time" title="信号撮合时间；悬浮显示信号发出时间与延迟（§买入点可追溯）">买入时间</span>
          <span class="col-num">数量</span>
          <span class="col-price">成本价</span>
          <span class="col-price">现价</span>
          <span class="col-chg">浮盈</span>
          <span class="col-chg">浮盈%</span>
          <span class="col-chg">滑点</span>
          <span class="col-num">延迟</span>
          <span class="col-pool">池</span>
          <span class="col-kline">分时</span>
          <span class="col-actions">操作</span>
        </div>
        <div v-for="p in filteredPositions" :key="p.code" class="pos-row-group">
          <div class="table-row" @click="onRowTap(p)">
            <span class="col-code" data-label="代码">{{ p.code }}</span>
            <span class="col-name" data-label="名称">{{ p.name }}</span>
            <span class="col-time" data-label="买入时间"
                  :title="'信号发出 ' + fmtTime(p.signal_at) + ' · 撮合成交 ' + fmtTime(p.filled_at)">
              {{ fmtTime(p.filled_at || p.signal_at) }}
            </span>
            <span class="col-num" data-label="数量">{{ p.qty }}</span>
            <span class="col-price" data-label="成本价">{{ p.cost_price.toFixed(2) }}</span>
            <span class="col-price" data-label="现价">{{ (p.mark || 0).toFixed(2) }}</span>
            <span :class="['col-chg', pnlCls(p.pnl)]" data-label="浮盈">{{ fmt(p.pnl) }}</span>
            <span :class="['col-chg', pnlCls(p.pnl)]" data-label="浮盈%">{{ fmt(p.pnl_pct) }}%</span>
            <span :class="['col-chg', pnlCls(p.slippage_pct)]" data-label="滑点">{{ fmt(p.slippage_pct) }}%</span>
            <span class="col-num" data-label="延迟">{{ p.latency_sec }}s</span>
            <span class="col-pool" data-label="池">
              <span class="tag">{{ poolLabel(p.strategy_type) }}</span>
            </span>
            <span class="col-kline" data-label="分时">
              <button class="btn-kline" @click.stop="toggleKline(p.code)" :title="klineOpen.has(p.code) ? '收起分时' : '展开分时'">
                {{ klineOpen.has(p.code) ? '收起' : '分时' }}
              </button>
            </span>
            <span class="col-actions" data-label="操作">
              <button class="btn-lot" @click.stop="openTrade(p, 'add')">加仓</button>
              <button class="btn-cost" @click.stop="openTrade(p, 'trim')">减仓</button>
              <button class="btn-sell" @click.stop="openTrade(p, 'close')">清仓</button>
            </span>
          </div>
          <!-- 展开的分时区（全宽，位于该行下方）（Expanded K-line area, full width, below the row）-->
          <div v-if="klineOpen.has(p.code)" class="pos-kline-row">
            <div class="kline-flex">
              <div class="kline-main"><KLineChart :code="p.code" :name="p.name" /></div>
              <div class="depth-side"><DepthPanel :code="p.code" :name="p.name" /></div>
            </div>
          </div>
        </div>
      </div>
      <div v-else class="empty-hint">
        {{ isAdmin ? '暂无持仓（出现可开仓信号时按实时价自动买入）' : '暂无持仓（在信号页点「模拟买入」，或上方加仓/减仓管理已有持仓）' }}
      </div>
    </div>

    <!-- 成交日志：div-grid（同模式：行内字段 + 分时展开 + 移动端 sheet）-->
    <div class="panel" v-if="tab === 'trades'">
      <div class="panel-title">成交日志 <em class="sub">{{ filteredTrades.length }} 笔 · 近3个月</em></div>
      <div class="positions-table" v-if="filteredTrades.length">
        <div class="table-header">
          <span class="col-time">时间</span>
          <span class="col-side">方向</span>
          <span class="col-code">代码</span>
          <span class="col-name">名称</span>
          <span class="col-pool">战法</span>
          <span class="col-num">数量</span>
          <span class="col-price">价格</span>
          <span class="col-price">金额</span>
          <span class="col-chg">滑点</span>
          <span class="col-num">延迟</span>
          <span class="col-kline">分时</span>
        </div>
        <div v-for="(t, i) in filteredTrades" :key="i" class="pos-row-group">
          <div class="table-row" @click="onTradeTap(t, i)">
            <span class="col-time" data-label="时间">{{ fmtTime(t.time) }}</span>
            <span class="col-side" data-label="方向">
              <span class="tag" :class="t.side === 'buy' ? 'buy' : 'sell'">{{ t.side === 'buy' ? '买入' : '卖出' }}</span>
            </span>
            <span class="col-code" data-label="代码">{{ t.code }}</span>
            <span class="col-name" data-label="名称">{{ t.name }}</span>
            <span class="col-pool" data-label="战法"><span class="tag">{{ t.strategy }}</span></span>
            <span class="col-num" data-label="数量">{{ t.qty }}</span>
            <span class="col-price" data-label="价格">{{ t.price.toFixed(2) }}</span>
            <span class="col-price" data-label="金额">{{ fmt(t.amount) }}</span>
            <span :class="['col-chg', tradeSlippageCls(t)]" data-label="滑点">{{ tradeSlippage(t) }}</span>
            <span class="col-num" data-label="延迟">{{ t.side === 'buy' ? (t.latency_sec || 0) + 's' : '—' }}</span>
            <span class="col-kline" data-label="分时">
              <button class="btn-kline" @click.stop="toggleKline('trade_' + i)">{{ klineOpen.has('trade_' + i) ? '收起' : '分时' }}</button>
            </span>
          </div>
          <div v-if="klineOpen.has('trade_' + i)" class="pos-kline-row">
            <div class="kline-flex">
              <div class="kline-main"><KLineChart :code="t.code" :name="t.name" /></div>
              <div class="depth-side"><DepthPanel :code="t.code" :name="t.name" /></div>
            </div>
          </div>
        </div>
      </div>
      <div v-else class="empty-hint">暂无成交记录</div>
    </div>

    <!-- 订单生命周期（阶段1.3）：信号→订单→成交/拒绝 全留痕，含被拒原因，便于复盘为何没买进/没卖出 -->
    <!-- Order lifecycle: full signal→order→outcome audit incl. rejections, so missed fills are reviewable -->
    <div class="panel" v-if="tab === 'orders'">
      <div class="panel-title">订单记录 <em class="sub">{{ filteredOrders.length }} 笔 · 含被拒留痕</em></div>
      <div class="positions-table" v-if="filteredOrders.length">
        <div class="table-header">
          <span class="col-time">时间</span>
          <span class="col-side">方向</span>
          <span class="col-code">代码</span>
          <span class="col-name">名称</span>
          <span class="col-pool">来源</span>
          <span class="col-num">状态</span>
          <span class="col-num">数量</span>
          <span class="col-price">成交价</span>
          <span class="col-price">信号价</span>
          <span class="col-name">说明</span>
        </div>
        <div v-for="(o, i) in filteredOrders" :key="o.id || i" class="table-row">
          <span class="col-time" data-label="时间">{{ fmtTime(o.created_at) }}</span>
          <span class="col-side" data-label="方向">
            <span class="tag" :class="o.side === 'buy' ? 'buy' : 'sell'">{{ o.side === 'buy' ? '买入' : '卖出' }}</span>
          </span>
          <span class="col-code" data-label="代码">{{ o.code }}</span>
          <span class="col-name" data-label="名称">{{ o.name }}</span>
          <span class="col-pool" data-label="来源"><span class="tag">{{ o.kind || '—' }}</span></span>
          <span class="col-num" data-label="状态">
            <span class="tag" :class="orderStatusCls(o.status)">{{ orderStatusText(o.status) }}</span>
          </span>
          <span class="col-num" data-label="数量">{{ o.qty || '—' }}</span>
          <span class="col-price" data-label="成交价">{{ o.price ? o.price.toFixed(2) : '—' }}</span>
          <span class="col-price" data-label="信号价">{{ o.signal_price ? o.signal_price.toFixed(2) : '—' }}</span>
          <span class="col-name" data-label="说明" :title="o.reason || ''">{{ shortReason(o.reason) }}</span>
        </div>
      </div>
      <div v-else class="empty-hint">暂无订单记录</div>
    </div>

    <!-- 移动端：点击行弹出的底部操作菜单（持仓）（Mobile bottom action sheet for a position row）-->
    <div class="sheet-overlay" v-if="sheetPos" @click="sheetPos = null">
      <div class="action-sheet" @click.stop>
        <div class="sheet-title">{{ sheetPos.code }} {{ sheetPos.name }}</div>
        <button class="sheet-btn" @click="sheetKline"> {{ klineOpen.has(sheetPos.code) ? '收起分时' : '展开分时' }}</button>
        <button class="sheet-btn" @click="sheetTrade('add')">加仓</button>
        <button class="sheet-btn" @click="sheetTrade('trim')">减仓</button>
        <button class="sheet-btn sheet-danger" @click="sheetTrade('close')">清仓</button>
        <button class="sheet-btn sheet-cancel" @click="sheetPos = null">取消</button>
      </div>
    </div>
    <!-- 移动端：成交行底部操作菜单（Mobile bottom action sheet for a fill row）-->
    <div class="sheet-overlay" v-if="sheetTradeRow" @click="sheetTradeRow = null">
      <div class="action-sheet" @click.stop>
        <div class="sheet-title">{{ sheetTradeRow.code }} {{ sheetTradeRow.name }}</div>
        <button class="sheet-btn" @click="sheetTradeKline">
          {{ klineOpen.has('trade_' + sheetTradeRow.idx) ? '收起分时' : '展开分时' }}
        </button>
        <button class="sheet-btn sheet-cancel" @click="sheetTradeRow = null">取消</button>
      </div>
    </div>

    <!-- 交易弹窗：加仓 / 减仓 / 清仓（输入价格 + 手数；照搬真实持仓页的加减仓模式）-->
    <div class="modal-overlay" v-if="tradeModal" @click.self="tradeModal = false">
      <div class="modal">
        <div class="modal-title">
          {{ tradeDir === 'add' ? '加仓' : (tradeDir === 'trim' ? '减仓' : '清仓') }}
          {{ tradeTarget?.code }} {{ tradeTarget?.name }}
        </div>
        <div class="form-row">
          <label>当前持仓</label>
          <span class="static-val">{{ tradeTarget?.qty }} 股 / 成本 ¥{{ tradeTarget?.cost_price?.toFixed(2) }}</span>
        </div>
        <div class="form-row">
          <label>价格</label>
          <input v-model.number="tradeFormPrice" type="number" step="0.001" placeholder="成交价格（留空用实时价）" />
        </div>
        <div class="form-row">
          <label>{{ tradeDir === 'add' ? '加仓手数' : (tradeDir === 'trim' ? '减仓手数' : '清仓') }}</label>
          <input v-if="tradeDir !== 'close'" v-model.number="tradeFormQty" type="number" step="1" placeholder="手数（1手=100股）" />
          <span v-else class="static-val">{{ tradeTarget?.qty }} 股（全部）</span>
        </div>
        <div class="preview" v-if="tradeDir === 'trim' && tradePreviewQty > 0">
          减仓后：剩余 {{ tradeTarget.qty - tradePreviewQty * 100 }} 股
        </div>
        <div class="modal-actions">
          <button class="btn-cancel" @click="tradeModal = false">取消</button>
          <button class="btn-confirm" :class="tradeDir === 'close' ? 'btn-confirm-sell' : ''"
                  @click="confirmTrade" :disabled="tradeOverSell">确定</button>
        </div>
      </div>
    </div>

    <!-- §反馈解耦 弹窗一：资金分配（仅动每池资金额，Σ=总现金守恒；不影响仓位上限）-->
    <div class="modal-overlay" v-if="allocOpen" @click.self="allocOpen = false">
      <div class="modal pool-config-modal">
        <div class="modal-title">资金分配 <em class="sub">（仅调整各池资金，仓位上限请在「仓位上限」中设置）</em></div>
        <div class="config-hint">
          每池资金额（元）。守恒校验：Σ池资金 ≈ 总现金；留空 = 自动均分剩余。
          不影响任何仓位上限设置。
        </div>
        <div v-for="p in pools" :key="'a-' + p.key" class="pool-config-row">
          <span class="pool-config-label">{{ p.label }}</span>
          <input v-model.number="cfgAllocs[p.key]" type="number" min="0" step="1000" class="cfg-input"
                 :placeholder="'当前 ¥' + fmt(p.cash)" title="该池资金额（空=自动均分剩余）" />
        </div>
        <div class="preview" v-if="cfgWarn">{{ cfgWarn }}</div>
        <div class="modal-actions">
          <button class="btn-cancel" @click="allocOpen = false">取消</button>
          <button class="btn-confirm" @click="savePoolAllocs">保存资金分配</button>
        </div>
      </div>
    </div>

    <!-- §反馈解耦 弹窗二：仓位上限（全局+每池，独立于资金）-->
    <div class="modal-overlay" v-if="capOpen" @click.self="capOpen = false">
      <div class="modal pool-config-modal">
        <div class="modal-title">仓位上限 <em class="sub">（全局 + 每池持仓数上限，不动资金分配）</em></div>
        <div class="form-row">
          <label>全局持仓上限</label>
          <input v-model.number="cfgMaxPos" type="number" min="0" step="1"
                 placeholder="0=不设限" :title="'当前生效 ' + appliedMax" />
          <span class="static-val">（0=不设限）</span>
        </div>
        <div class="config-hint">每池持仓上限（0=不单独设限）；守恒：Σ池上限 ≤ 全局上限。</div>
        <div v-for="p in pools" :key="'c-' + p.key" class="pool-config-row">
          <span class="pool-config-label">{{ p.label }}</span>
          <input v-model.number="cfgCaps[p.key]" type="number" min="0" step="1" class="cfg-input cfg-cap"
                 :placeholder="p.max_pos > 0 ? '当前 ' + p.max_pos : '不单独设限'" />
        </div>
        <div class="preview" v-if="cfgWarn">{{ cfgWarn }}</div>
        <div class="modal-actions">
          <button class="btn-cancel" @click="capOpen = false">取消</button>
          <button class="btn-confirm" @click="savePoolCaps">保存仓位上限</button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
// ── 依赖导入 ── (Imports)
import { ref, computed, onMounted, onUnmounted } from 'vue' // Vue 组合式 API（响应式与生命周期）
import * as api from '../api/index.js' // 后端 API 封装（模拟盘接口）
import KLineChart from '../components/KLineChart.vue' // 分时图组件（展开行展示）
import DepthPanel from '../components/DepthPanel.vue' // 盘口面板（展开行展示，买卖五档）

// ── 状态 ── (State)
const enabled = ref(false)       // 模拟盘总开关
const isAdmin = ref(false)       // admin 账户标记（联动版：自动撮合/回测/盘后研究导出）
const initialCapital = ref('')   // 自定义初始资金输入（确认资金/清盘重置时生效）
const maxPos = ref('')           // 自定义持仓上限输入（0=不设限）
const appliedMax = ref(0)        // 当前生效的持仓上限（经确认资金固化；0=不设限，header 展示用）
const tab = ref('positions')     // 页签：positions=持仓 / trades=成交日志
const stats = ref(null)          // 绩效与信号质量汇总
const positions = ref([])        // 当前持仓
const trades = ref([])           // 成交记录
const orders = ref([])           // 订单生命周期（阶段1.3：信号→订单→成交/拒绝 全留痕）
const equity = ref([])           // 净值序列
const pools = ref([])            // 分仓资金池快照（strategy_pools）
const activePool = ref(null)     // 当前筛选的分仓（null=全部；池 key，""=其他/手动用 '__other__'）
const W = 900, H = 220           // 净值折线 SVG 画布尺寸
let timer = null                 // 轮询定时器
// ── 分仓配置弹窗（每池资金/上限自定义，与全局解耦，总和守恒）── (Pool-config modal)
const showDepositModal = ref(false)
	// 清盘确认弹窗开关
const showResetModal = ref(false)
	// 统一设置弹窗（资金分配/仓位上限）开关
const settingsOpen = ref(false)
	// 设置弹窗当前 tab：alloc 资金分配 / caps 仓位上限
const settingsTab = ref('alloc')
	// 注入资金弹窗金额
const depositAmount = ref(0)
	// 清盘重置后的初始资金额（0=默认100000）
const resetToCapital = ref(0)
	// 清盘重置时的持仓上限（0=不设限）
const resetMaxPos = ref(0)
const cfgMaxPos = ref(0)            // 弹窗内全局持仓上限
const cfgAllocs = ref({})           // 每池目标资金额（key=策略类型；空=自动均分剩余）
const cfgCaps = ref({})             // 每池持仓上限（key=策略类型；0=不单独设限）
const cfgRules = ref({})            // §A3 每池买入纪律（key→四字段；全零=清除该池规则）
const cfgRuleSel = ref('')          // §A3 纪律 tab 当前选中的池 key（下拉切换，只渲染单池）
const cfgWarn = ref('')             // 守恒校验提示（Σ资金/Σ上限超限时警示）

/** 该池后端当前生效的纪律规则（预填对照）。 */
function poolCurrentRule(key) {
  const p = pools.value.find(x => x.key === key)
  return !!(p && p.buy_rule && (p.buy_rule.max_daily_buys || p.buy_rule.cooldown_minutes ||
    p.buy_rule.min_score || p.buy_rule.budget_pct_per_day))
}
function poolCurrentRuleText(key) {
  const p = pools.value.find(x => x.key === key)
  const r = p && p.buy_rule
  if (!r) return ''
  return `限${r.max_daily_buys||'∞'}次/冷却${r.cooldown_minutes||0}分/分≥${r.min_score||0}/预算${r.budget_pct_per_day||0}%`
}

// ── 分时展开 / 移动端 sheet（照搬真实持仓页）── (K-line expand / mobile sheet, ported from Positions)
const klineOpen = ref(new Set())      // 已展开分时的行键集合（持仓=code，成交='trade_'+i）
const sheetPos = ref(null)            // 移动端：被点击的持仓行
const sheetTradeRow = ref(null)       // 移动端：被点击的成交行
// ── 交易弹窗（加仓/减仓/清仓）── (Trade modal: add / trim / close)
const tradeModal = ref(false)         // 交易弹窗开关
const tradeDir = ref('add')           // add / trim / close
const tradeTarget = ref(null)         // 目标持仓
const tradeFormPrice = ref(0)         // 输入价格（0=用实时价）
const tradeFormQty = ref(1)           // 输入手数（1手=100股）
// // 模拟下单预览：按置信度×止损宽度折算建议股数
const tradePreviewQty = computed(() => {
  const q = parseInt(tradeFormQty.value, 10)
  return isNaN(q) || q <= 0 ? 0 : q
})
// // 超卖校验：请求股数超过可用现金可买的上限时提示
const tradeOverSell = computed(() =>
  tradeDir.value === 'trim' && tradeTarget.value && tradePreviewQty.value * 100 >= tradeTarget.value.qty
)

// ── 净值折线 ── (Equity line)
// 把净值点映射为 SVG polyline 坐标（首末留白，Y 轴按最小值缩放）
// Map equity points to SVG polyline coordinates (with padding; Y scaled from the min)
const linePoints = computed(() => {
  if (equity.value.length < 2) return ''
  const pad = 10
  const vals = equity.value.map(p => p.value)
  const min = Math.min(...vals), max = Math.max(...vals)
  const range = max - min || 1
  return equity.value.map((p, i) => {
    const x = pad + (i / (equity.value.length - 1)) * (W - 2 * pad)
    const y = H - pad - ((p.value - min) / range) * (H - 2 * pad)
    return x.toFixed(1) + ',' + y.toFixed(1)
  }).join(' ')
})

// 三条横向网格线（1/2/3 位置）
const gridLines = computed(() => [1, 2, 3].map(k => ({ y: (H / 4) * k })))

// ── 工具函数 ── (Helpers)
// 数字格式化：千分位 + 两位小数（thousands separator, two decimals）
function fmt(v) { return (v ?? 0).toLocaleString('zh-CN', { minimumFractionDigits: 2, maximumFractionDigits: 2 }) }
// 时间格式化：MM-DD HH:mm:ss（§买入点可追溯；ISO 串优先 Date 解析，异常回退截断）
function fmtTime(t) {
  if (!t) return '—'
  const d = new Date(t)
  if (isNaN(d)) return t.slice(5, 16)
  const p2 = n => String(n).padStart(2, '0')
  return `${p2(d.getMonth() + 1)}-${p2(d.getDate())} ${p2(d.getHours())}:${p2(d.getMinutes())}:${p2(d.getSeconds())}`
}
// 涨跌颜色类：非负红（A股习惯红涨），负绿（positive = red per A-share convention）
function pnlCls(v) { return v >= 0 ? 'up' : 'down' }
// 单笔成交滑点%：（成交价 - 信号价）/ 信号价（仅买入有信号价参照；卖出/无信号价显示 —）
// English: per-fill slippage % — (fill - signal) / signal, only meaningful on buys (sells show —).
function tradeSlippage(t) {
  if (t.side !== 'buy' || !(t.signal_price > 0)) return '—'
  const pct = (t.price - t.signal_price) / t.signal_price * 100
  return (pct >= 0 ? '+' : '') + pct.toFixed(2) + '%'
}
// // 个股滑点配色：滑点越大越红
function tradeSlippageCls(t) {
  if (t.side !== 'buy' || !(t.signal_price > 0)) return ''
  return t.price >= t.signal_price ? 'down' : 'up'
}
// 战法池展示名（空=其他/手动；与后端 strategyPoolLabel 保持一致。§命名纠错：dragon=龙头。
// §名称规整：n_shape 统一"N形"，新增 momentum=动量）
function poolLabel(k) {
  if (!k) return '其他/手动'
  const labels = { dragon: '龙头', double_bump: '双响炮', n_shape: 'N形', dragon_return: '龙回头', momentum: '动量' }
  if (labels[k]) return labels[k]
  if (/^fac_/.test(k)) return '因子·' + k
  if (/^pat_/.test(k)) return '形态·' + k
  return k
}

// 池 key 规范化：空串（其他/手动池）映射为占位 '__other__'，避免与"全部(null)"冲突。
// English: normalizes a pool key — the empty key (other/manual pool) maps to a sentinel so it never
// collides with "all (null)".
function normPoolKey(k) { return k || '__other__' }
// 点按分仓 tab 切换筛选：再次点击当前池 = 回到全部。
// English: toggles the pool filter; tapping the active pool again resets to all.
function togglePool(k) {
  const key = normPoolKey(k)
  activePool.value = activePool.value === key ? null : key
}
// 按当前分仓筛选持仓 / 成交（全部时不筛选）。
// English: filters positions / fills by the active pool (no filter when all).
const filteredPositions = computed(() => {
  if (activePool.value === null) return positions.value
  return positions.value.filter(p => normPoolKey(p.strategy_type) === activePool.value)
})
// // 成交流水过滤（按当前 tab 的池/战法筛选）
const filteredTrades = computed(() => {
  if (activePool.value === null) return trades.value
  return trades.value.filter(t => normPoolKey(t.strategy_type) === activePool.value)
})
// 订单生命周期（随分仓 tab 筛选，与成交日志同口径）
// Order lifecycle (pool-filtered, same semantics as the trade log)
const filteredOrders = computed(() => {
  if (activePool.value === null) return orders.value
  return orders.value.filter(o => normPoolKey(o.strategy_type) === activePool.value)
})
/** 订单状态中文标签 */
function orderStatusText(s) {
  return { filled: '全部成交', partial: '部分成交', rejected: '已拒绝' }[s] || s
}
/** 订单状态徽标色：成交绿 / 部分黄 / 拒绝红 */
function orderStatusCls(s) {
  return { filled: 'buy', partial: 'hold', rejected: 'sell' }[s] || 'hold'
}
/** 说明列截断（完整内容悬停 title 展示） */
function shortReason(r) {
  if (!r) return '—'
  return r.length > 18 ? r.slice(0, 18) + '…' : r
}
// 当前展示的统计：选中的分仓池用自己的 Stats（总资产/收益/滑点/延迟等随 tab 切换），
// 未选择（全部）时用全账号 stats。
// English: the stats currently shown — a selected pool uses its own Stats (total value / return /
// slippage / latency follow the tab), otherwise the whole-account stats.
const activeStats = computed(() => {
  if (activePool.value === null) return stats.value
  const p = pools.value.find(p => normPoolKey(p.key) === activePool.value)
  return (p && p.stats) || stats.value
})
// 当前选中分仓的展示名（统计范围标签用）。
// English: display label of the selected pool (for the stats-scope tag).
const activePoolLabel = computed(() => {
  const p = pools.value.find(p => normPoolKey(p.key) === activePool.value)
  return p ? p.label : ''
})

// ── 分时 / sheet 交互（照搬真实持仓页）── (K-line & sheet interactions, ported from Positions)
// 展开/收起某行的分时区
function toggleKline(key) {
  const next = new Set(klineOpen.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  klineOpen.value = next
}
// 移动端：点击持仓行打开操作菜单
function onRowTap(p) {
  if (window.innerWidth > 768) return
  sheetPos.value = p
}
// 移动端：点击成交行打开操作菜单
function onTradeTap(t, i) {
  if (window.innerWidth > 768) return
  sheetTradeRow.value = { ...t, idx: i }
}
// 移动端：操作菜单 - 展开/收起持仓分时
function sheetKline() {
  if (!sheetPos.value) return
  toggleKline(sheetPos.value.code)
  sheetPos.value = null
}
// 移动端：操作菜单 - 加仓/减仓/清仓
function sheetTrade(dir) {
  if (!sheetPos.value) return
  const p = sheetPos.value
  sheetPos.value = null
  openTrade(p, dir)
}
// 移动端：成交行 - 展开/收起分时
function sheetTradeKline() {
  if (!sheetTradeRow.value) return
  toggleKline('trade_' + sheetTradeRow.value.idx)
  sheetTradeRow.value = null
}

// 打开交易弹窗（加仓/减仓/清仓）：回填当前持仓
function openTrade(p, dir) {
  tradeTarget.value = p
  tradeDir.value = dir
  tradeFormPrice.value = p.mark || 0
  tradeFormQty.value = dir === 'close' ? p.qty : 1
  tradeModal.value = true
}

// 提交交易：加仓走 buy（BuyEx 已持仓自动合并）；减仓/清仓走 sell（SellEx 支持指定数量/价格）
async function confirmTrade() {
  const p = tradeTarget.value
  if (!p) return
  const price = parseFloat(tradeFormPrice.value)
  const qty = parseInt(tradeFormQty.value, 10)
  if (tradeDir.value !== 'close' && (isNaN(qty) || qty <= 0)) { alert('请输入有效的数量'); return }
  try {
    if (tradeDir.value === 'add') {
      await api.buyPaperPosition(p.code, p.name || '', p.strategy || '', 0, price > 0 ? price : 0, qty)
      alert(`已加仓 ${p.code} ${qty} 手`)
    } else {
      await api.sellPaperPosition(p.code, price > 0 ? price : 0, tradeDir.value === 'close' ? 0 : qty)
      alert(`已${tradeDir.value === 'close' ? '清仓' : '减仓'} ${p.code}`)
    }
    tradeModal.value = false
    await load()
  } catch (e) { alert(e.message || '操作失败') }
}

// ── 数据加载 ── (Data loading)
// 拉取模拟盘全量状态（开关/统计/分仓/持仓/成交/净值），失败时静默保留旧数据
// Fetch the full paper state; keep stale data on failure
async function load() {
  try {
    const st = await api.fetchPaperState()
    enabled.value = !!st.enabled
    isAdmin.value = !!st.is_admin
    if (st.initial_capital > 0 && !initialCapital.value) initialCapital.value = String(st.initial_capital)
    if (st.max_positions !== undefined && !maxPos.value) maxPos.value = st.max_positions > 0 ? String(st.max_positions) : '0'
    appliedMax.value = (st.max_positions !== undefined && st.max_positions > 0) ? st.max_positions : 0
    stats.value = st.stats || null
    pools.value = Array.isArray(st.strategy_pools) ? st.strategy_pools : []
  } catch (_) {}
  if (!enabled.value) return
  try { positions.value = await api.fetchPaperPositions() } catch (_) {}
  try { trades.value = await api.fetchPaperTrades() } catch (_) {}
  try { orders.value = await api.fetchPaperOrders() } catch (_) {}
  try { equity.value = await api.fetchPaperEquity() } catch (_) {}
}

// ── 操作 ── (Actions)
// 注入资金：输入注入金额（可选持仓上限）后确认，增量加现金并保留现有持仓/净值/成交日志
// （与真实持仓一致：加钱不清仓，收益基准=累计投入）。
// Deposit: enter the amount to add (and optional position cap), confirm, then cash increases
// incrementally while positions / equity / fill log are all kept (just like the real book: adding
// money never clears holdings; the return basis is the cumulative investment).
async function confirmDeposit() {
  const amt = parseFloat(initialCapital.value)
  if (!(amt > 0)) { alert('请输入有效的注入金额'); return }
  const mp = parseInt(maxPos.value, 10)
  const mpv = mp > 0 ? mp : 0
  const capHint = mpv > 0 ? '，持仓上限 ' + mpv + ' 只' : '（持仓上限不设限，由资金决定）'
  if (!confirm('确认注入资金 ¥' + fmt(amt) + capHint + '？将增量计入现金，保留现有持仓/净值/成交记录。')) return
  try {
    const res = await api.resetPaper(amt, mpv)
    // 注入成功后同步输入框与当前生效上限，避免轮询 load 覆盖显示旧值
    // After a successful deposit, sync the inputs and the applied cap so polling doesn't show stale values
    initialCapital.value = String(res.initial_capital || (parseFloat(initialCapital.value) + amt))
    maxPos.value = String(res.max_positions > 0 ? res.max_positions : 0)
    appliedMax.value = res.max_positions > 0 ? res.max_positions : 0
    await load()
  } catch (e) { alert(e.message || '注入失败') }
}

// 单池清盘：只清当前选中分仓池的持仓与累计表现（平仓回池现金），其余池与全局净值/成交不受影响。
// Reset a single pool: closes only the selected pool's positions & persisted perf (proceeds return to
// the pool); other pools and the global equity/fill log are untouched.
async function confirmPoolReset() {
  if (activePool.value === null) return
  const label = activePoolLabel.value
  const count = filteredPositions.value.length
  if (!confirm(`清盘「${label}」资金池？\n将按最后估值价平仓该池 ${count} 笔持仓（回补池现金），并清空该池累计涨跌幅表现。\n其他分仓资金池与全局净值/成交日志不受影响。`)) return
  try {
    await api.resetPaperPool(activePool.value === '__other__' ? '' : activePool.value)
    await load()
  } catch (e) { alert(e.message || '清盘失败') }
}

// 打开统一设置弹窗：回填全部当前值
function openSettingsModal() {
  cfgMaxPos.value = appliedMax.value > 0 ? appliedMax.value : 0
  cfgAllocs.value = {}
  cfgCaps.value = {}
  cfgRules.value = {}
  pools.value.forEach(p => {
    cfgAllocs.value[p.key] = p.cash
    cfgCaps.value[p.key] = p.max_pos || 0
    // §A3 纪律预填：池快照带当前生效规则（nil=全零）
    cfgRules.value[p.key] = {
      max_daily_buys: (p.buy_rule && p.buy_rule.max_daily_buys) || 0,
      cooldown_minutes: (p.buy_rule && p.buy_rule.cooldown_minutes) || 0,
      min_score: (p.buy_rule && p.buy_rule.min_score) || 0,
      budget_pct_per_day: (p.buy_rule && p.buy_rule.budget_pct_per_day) || 0,
    }
  })
  // §A3 下拉默认选第一个池（跳过"其他"池优先展示战法池）
  const firstStrategy = pools.value.find(p => p.key !== '')
  cfgRuleSel.value = firstStrategy ? firstStrategy.key : (pools.value[0] ? pools.value[0].key : '')
  settingsTab.value = 'alloc'
  cfgWarn.value = ''
  settingsOpen.value = true
}

// 统一保存设置：按当前 tab 只提交对应字段（互不影响）。
async function saveSettings() {
  const totalCash = pools.value.reduce((s, p) => s + p.cash, 0)

  if (settingsTab.value === 'alloc') {
    const allocs = {}
    let assigned = 0
    pools.value.forEach(p => {
      const n = parseFloat(cfgAllocs.value[p.key])
      if (n > 0) { allocs[p.key] = n; assigned += n }
    })
    if (assigned > totalCash + 0.01) {
      cfgWarn.value = `资金超额：Σ ¥${fmt(assigned)} > 总现金 ¥${fmt(totalCash)}`
      return
    }
    try {
      await api.configPaperPools(null, null, allocs)
      settingsOpen.value = false
      await load()
    } catch (e) { alert(e.message || '保存失败') }
    return
  }

  // rules tab（§A3）：四字段全零=清除该池规则（后端按"全零→SetPoolBuyRule(nil)"语义）
  if (settingsTab.value === 'rules') {
    const rules = {}
    pools.value.forEach(p => {
      const r = cfgRules.value[p.key] || {}
      rules[p.key] = {
        max_daily_buys: parseInt(r.max_daily_buys, 10) || 0,
        cooldown_minutes: parseInt(r.cooldown_minutes, 10) || 0,
        min_score: parseFloat(r.min_score) || 0,
        budget_pct_per_day: parseFloat(r.budget_pct_per_day) || 0,
      }
    })
    try {
      await api.configPaperPools(null, null, null, rules)
      settingsOpen.value = false
      await load()
    } catch (e) { alert(e.message || '保存失败') }
    return
  }

  // caps tab
  const caps = {}
  let capSum = 0
  pools.value.forEach(p => {
    const c = parseInt(cfgCaps.value[p.key], 10)
    if (c > 0) { caps[p.key] = c; capSum += c }
  })
  const gCap = parseInt(cfgMaxPos.value, 10)
  if (gCap > 0 && capSum > gCap) {
    cfgWarn.value = `Σ池上限 ${capSum} > 全局 ${gCap}`
    return
  }
  try {
    await api.configPaperPools(gCap, caps, null)
    settingsOpen.value = false
    await load()
  } catch (e) { alert(e.message || '保存失败') }
}

// 恢复均分：清除每池自定义资金。
async function clearPoolAllocs() {
  await api.configPaperPools(null, {}, {})
}

// 恢复均分：清除每池自定义资金（显式传空对象触发后端 ResetPoolAllocs）。
async function clearAllocs() {
  if (!confirm('清除每池自定义资金并恢复均分？仓位上限不受影响。')) return
  try {
    await api.configPaperPools(null, {}, {})
    await load()
  } catch (e) { alert(e.message || '操作失败') }
}

// 清盘重置：仅清仓并按配置初始资金重置，不修改自定义资金
// Reset: liquidate everything and reset to the configured capital, without changing custom settings
async function doResetV2() {
  if (!confirm('确认清盘？\n将平仓全部持仓、清除成交日志与净值曲线。')) return
  try {
    const body = {}
    if (resetToCapital.value > 0) body.reset_to = resetToCapital.value
    if (resetMaxPos.value > 0) body.max_positions = resetMaxPos.value
    await api.paperResetV2(body)
    showResetModal.value = false
    resetToCapital.value = 0
    resetMaxPos.value = 0
    await load()
  } catch (e) { alert(e.message || '清盘失败') }
}

// ── 生命周期 ── (Lifecycle)
onMounted(() => {
  load()
  timer = setInterval(load, 15000) // 15s 轮询刷新（持仓现价/净值随实时行情变化）
})
onUnmounted(() => { if (timer) clearInterval(timer) })
</script>

<style scoped>
/* ── 页面与面板 ── (Page & panels) */
.paper-page { padding: 20px; max-width: 1200px; margin: 0 auto; }
.page-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 16px; flex-wrap: wrap; gap: 8px; }
.page-header h2 { margin: 0; font-size: 20px; }
.header-right { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.enabled-badge { font-size: 12px; padding: 3px 10px; border-radius: 10px; }
.enabled-badge.on { background: rgba(82, 196, 26, 0.15); color: #52c41a; }
.enabled-badge.off { background: rgba(255, 255, 255, 0.06); color: #8fa3bf; }
.admin-badge { font-size: 12px; padding: 3px 10px; border-radius: 10px; background: rgba(255, 213, 79, 0.15); color: #FFD54F; }
.cap-badge { font-size: 12px; padding: 3px 10px; border-radius: 10px; background: rgba(255, 255, 255, 0.04); color: #8fa3bf; border: 1px solid rgba(255, 255, 255, 0.1); white-space: nowrap; }
.cap-input { background: #16162a; color: #e6edf3; border: 1px solid rgba(255, 255, 255, 0.12); border-radius: 6px; padding: 6px 10px; font-size: 13px; width: 120px; }
.cap-input:disabled { opacity: 0.4; cursor: not-allowed; }
.cap-max { width: 90px; }
.btn-confirm { background: rgba(82, 196, 26, 0.15); color: #52c41a; border: 1px solid rgba(82, 196, 26, 0.4); padding: 6px 14px; border-radius: 6px; cursor: pointer; font-size: 13px; }
.btn-confirm:disabled { opacity: 0.4; cursor: not-allowed; }
.btn-reset { background: rgba(255, 77, 79, 0.12); color: #FF4D4F; border: 1px solid rgba(255, 77, 79, 0.35); padding: 6px 14px; border-radius: 6px; cursor: pointer; font-size: 13px; }
.btn-reset:disabled { opacity: 0.4; cursor: not-allowed; }
.btn-config { background: rgba(124, 77, 255, 0.12); color: #b388ff; border: 1px solid rgba(124, 77, 255, 0.4); padding: 6px 14px; border-radius: 6px; cursor: pointer; font-size: 13px; }
.btn-config:disabled { opacity: 0.4; cursor: not-allowed; }
.tabs { display: flex; gap: 8px; margin-bottom: 16px; }
.tab { background: rgba(255, 255, 255, 0.04); color: #8fa3bf; border: 1px solid rgba(255, 255, 255, 0.1); padding: 8px 16px; border-radius: 8px; cursor: pointer; font-size: 13px; }
.tab.active { background: rgba(255, 77, 79, 0.12); color: #FF4D4F; border-color: rgba(255, 77, 79, 0.4); }
.panel { background: #1b1b30; border-radius: 10px; padding: 16px; margin-bottom: 16px; }
.panel-title { font-size: 15px; font-weight: 600; margin-bottom: 12px; }
.sub { font-size: 12px; font-weight: 400; color: #8fa3bf; font-style: normal; }
.empty-hint { color: #8fa3bf; font-size: 13px; padding: 12px 0; text-align: center; }

/* ── 分仓资金池条 ── (Strategy-pool allocation strip) */
.pools-bar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; background: #1b1b30; border-radius: 10px; padding: 12px 14px; margin-bottom: 16px; }
.pools-title { font-size: 12px; color: #8fa3bf; margin-right: 4px; white-space: nowrap; }
.pool-chip { display: inline-flex; align-items: center; gap: 6px; background: rgba(255, 255, 255, 0.04); border: 1px solid rgba(255, 255, 255, 0.1); border-radius: 8px; padding: 6px 10px; font-size: 12px; cursor: pointer; user-select: none; }
.pool-chip:hover { border-color: rgba(124, 77, 255, 0.5); }
.pool-chip.active { background: rgba(124, 77, 255, 0.14); border-color: #7c4dff; box-shadow: 0 0 0 1px rgba(124, 77, 255, 0.4); }
.pool-chip.other { border-style: dashed; opacity: 0.85; }
.pool-chip.other.active { opacity: 1; }
.pool-label { color: #b388ff; font-weight: 600; }
.pool-return { font-weight: 600; }
.pool-return.up { color: #FF4D4F; }
.pool-return.down { color: #52c41a; }
.pool-cash { color: #e6edf3; }
.pool-meta { color: #8fa3bf; }
.btn-pool-reset { background: rgba(255, 77, 79, 0.12); color: #FF4D4F; border: 1px solid rgba(255, 77, 79, 0.35); padding: 6px 14px; border-radius: 8px; cursor: pointer; font-size: 12px; margin-left: auto; white-space: nowrap; }
.btn-pool-reset:hover { background: rgba(255, 77, 79, 0.22); }
.btn-pool-reset:disabled { opacity: 0.4; cursor: not-allowed; }

/* ── 统计范围标签（跟随分仓 tab）── (Stats-scope tag, follows the pool tab) */
.stats-scope { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
.stats-scope-tag { font-size: 12px; color: #b388ff; background: rgba(124, 77, 255, 0.12); border: 1px solid rgba(124, 77, 255, 0.35); border-radius: 8px; padding: 3px 10px; }

/* ── 统计卡 ── (Stat cards) */
.stats-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(160px, 1fr)); gap: 10px; margin-bottom: 16px; }
.stats-grid.quality { margin-bottom: 8px; }
.stat-card { background: #1b1b30; border-radius: 10px; padding: 12px 14px; }
.stat-label { font-size: 12px; color: #8fa3bf; margin-bottom: 6px; }
.stat-value { font-size: 18px; font-weight: 600; }
.stat-value em.sub { display: block; font-size: 11px; margin-top: 2px; }

/* ── 涨跌颜色（A股红涨绿跌）── (A-share color: red up / green down) */
.up { color: #FF4D4F; }
.down { color: #52c41a; }

/* ── 净值折线 ── (Equity chart) */
.equity-chart { width: 100%; height: 220px; background: #16162a; border-radius: 8px; }
.grid-line { stroke: rgba(255, 255, 255, 0.06); stroke-width: 1; }

/* ── 数据表（div-grid，照搬真实持仓页）── (div-grid, ported from the real positions page) */
.positions-table { background: #16162a; border-radius: 8px; overflow-x: auto; font-size: 13px; white-space: nowrap; }
.table-header, .table-row { display: flex; align-items: center; padding: 9px 14px; gap: 0; min-width: 1240px; }
.table-header { background: #22223a; color: #8fa3bf; font-weight: 600; }
.pos-row-group { border-bottom: 1px solid #22223a; min-width: 1240px; }
.pos-row-group:last-child { border-bottom: none; }
.table-row:hover { background: rgba(255, 255, 255, 0.03); }
.col-code  { flex: 1; color: #4fc3f7; text-align: center; }
.col-name  { flex: 1; overflow: hidden; text-overflow: ellipsis; }
.col-num   { flex: 1; text-align: center; }
.col-price { flex: 1; text-align: center; }
.col-chg   { flex: 1; text-align: center; }
.col-chg.up, .up { color: #FF4D4F; font-weight: 600; }
.col-chg.down, .down { color: #52c41a; font-weight: 600; }
.col-pool  { flex: 1; text-align: center; }
.col-time  { flex: 1; text-align: center; }
.col-side  { flex: 1; text-align: center; }
.col-kline { flex: 0 0 64px; text-align: center; }
.btn-kline { background: transparent; border: 1px solid #3a3a55; color: #7ab8ff; border-radius: 4px; cursor: pointer; font-size: 12px; padding: 2px 8px; }
.btn-kline:hover { border-color: #4fc3f7; color: #4fc3f7; }
.pos-kline-row { padding: 8px 14px 12px; background: #14142a; }
.kline-flex { display: flex; gap: 12px; align-items: stretch; }
.kline-main { flex: 1 1 auto; min-width: 0; }
.depth-side { flex: 0 0 300px; }
@media (max-width: 720px) {
  .kline-flex { flex-direction: column; }
  .depth-side { flex: 1 1 auto; }
}
.col-actions { display: flex; gap: 4px; flex: 0 0 200px; justify-content: center; }
.btn-edit, .btn-sell, .btn-lot, .btn-cost { padding: 3px 10px; border-radius: 4px; font-size: 12px; cursor: pointer; white-space: nowrap; }
.btn-lot { border: 1px solid #7c4dff; background: transparent; color: #b388ff; }
.btn-lot:hover { background: rgba(124, 77, 255, 0.12); }
.btn-cost { border: 1px solid #FAAD14; background: transparent; color: #FAAD14; }
.btn-cost:hover { background: rgba(250, 173, 20, 0.1); }
.btn-sell { border: 1px solid #FAAD14; background: transparent; color: #FAAD14; }
.btn-sell:hover { background: rgba(250, 173, 20, 0.1); }
.tag { display: inline-block; padding: 1px 8px; border-radius: 8px; background: rgba(255, 255, 255, 0.08); font-size: 12px; }
.tag.buy { background: rgba(255, 77, 79, 0.15); color: #FF4D4F; }
.tag.sell { background: rgba(82, 196, 26, 0.15); color: #52c41a; }

/* ── 弹窗（照搬真实持仓页）── (Modals, ported from the real positions page) */
.modal-overlay { position: fixed; top: 0; left: 0; width: 100%; height: 100%; background: rgba(0, 0, 0, 0.6); display: flex; align-items: center; justify-content: center; z-index: 100; }
.modal { background: #1a1a2e; border-radius: 10px; padding: 24px; width: 380px; }
.modal-title { font-size: 16px; font-weight: 600; color: #e0e0e0; margin-bottom: 16px; }
.form-row { margin-bottom: 12px; display: flex; align-items: center; gap: 8px; }
.form-row label { width: 80px; color: #888; font-size: 14px; flex-shrink: 0; }
.form-row input { flex: 1; padding: 8px 12px; border-radius: 6px; border: 1px solid #333; background: #0f0f23; color: #e0e0e0; font-size: 14px; outline: none; }
.form-row input:focus { border-color: #FF4D4F; }
.static-val { color: #e0e0e0; font-size: 14px; white-space: nowrap; }
.preview { margin: 4px 0 8px 88px; font-size: 14px; color: #b388ff; }
.config-hint { font-size: 12px; color: #8fa3bf; margin: 4px 0 10px; line-height: 1.6; }
.pool-config-row { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
.pool-config-label { width: 90px; color: #b388ff; font-weight: 600; font-size: 13px; flex-shrink: 0; }
.cfg-input { flex: 1; padding: 6px 10px; border-radius: 6px; border: 1px solid #333; background: #0f0f23; color: #e0e0e0; font-size: 13px; outline: none; min-width: 0; }
.cfg-input:focus { border-color: #FF4D4F; }
/* §A3 买入纪律 tab：下拉选池 + 单池 2×2 字段网格（避免随战法增多无限平铺） */
.cfg-select { width:100%; padding:6px 10px; border-radius:6px; border:1px solid #333; background:#0f0f23; color:#e0e0e0; font-size:13px; outline:none; margin-bottom:8px; }
.cfg-select:focus { border-color:#FF4D4F; }
.pool-rules-row { margin-bottom: 10px; padding: 10px; border: 1px solid #2a2a4a; border-radius: 8px; }
.pool-rules-head { color: #b388ff; font-weight: 600; font-size: 13px; margin-bottom: 6px; }
.rules-now { color:#888; font-weight:400; font-size:11px; margin-left:6px; }
.pool-rules-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 6px 10px; }
.pool-rules-grid label { display: flex; align-items: center; gap: 6px; font-size: 12px; color: #aaa; }
.pool-rules-grid input { width: 70px; padding: 4px 8px; border-radius: 6px; border: 1px solid #333; background: #0f0f23; color: #e0e0e0; font-size: 12px; outline: none; }
.pool-rules-grid input:focus { border-color: #FF4D4F; }
.cfg-cap { max-width: 120px; }
.pool-config-modal { width: 480px; }
.modal-actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 16px; }
.btn-cancel { padding: 8px 20px; border-radius: 6px; border: 1px solid #333; background: transparent; color: #888; font-size: 14px; cursor: pointer; }
.btn-confirm { padding: 8px 20px; border-radius: 6px; border: none; background: #FF4D4F; color: #fff; font-size: 14px; cursor: pointer; }
.btn-confirm:disabled { opacity: 0.5; cursor: not-allowed; }
.btn-confirm-sell { background: #52c41a; }

/* ── 移动端：横向滚动 + sheet ── (Mobile: horizontal scroll + bottom action sheet) */
@media (max-width: 768px) {
  .positions-table { overflow-x: auto; white-space: nowrap; -webkit-overflow-scrolling: touch; }
  .table-header, .table-row { min-width: 1160px; padding: 9px 12px; }
  .pos-row-group { min-width: 0; }
  .page-header { flex-direction: column; align-items: stretch; gap: 8px; }
  .header-right { flex-wrap: wrap; gap: 8px; }
  .modal { width: 92%; max-width: 380px; padding: 18px; }
  .form-row { flex-wrap: wrap; }
  .form-row label { width: 70px; }
  .preview { margin-left: 0; }
  .table-row { cursor: pointer; }
  .sheet-overlay { position: fixed; inset: 0; z-index: 300; background: rgba(0, 0, 0, 0.6); display: flex; align-items: flex-end; }
  .action-sheet { width: 100%; background: #1a1a2e; border-radius: 14px 14px 0 0; padding: 10px 12px calc(10px + env(safe-area-inset-bottom, 0px)); }
  .sheet-title { font-size: 14px; color: #999; text-align: center; padding: 8px 0 12px; border-bottom: 1px solid #2a2a3e; margin-bottom: 8px; }
  .sheet-btn { width: 100%; padding: 14px; border-radius: 8px; border: none; background: #0f0f23; color: #4fc3f7; font-size: 16px; cursor: pointer; margin-bottom: 8px; text-align: center; }
  .sheet-btn:active { opacity: 0.8; }
  .sheet-danger { color: #FF4D4F; }
  .sheet-cancel { background: #2a2a3e; color: #888; }
}
</style>