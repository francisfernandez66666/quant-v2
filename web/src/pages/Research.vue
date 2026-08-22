<!--
  自动研究页 Research.vue（B5）
  Auto-research page Research.vue (B5)
  展示优化器产出的候选列表，支持审批通过（应用权重）/ 驳回。
  Shows optimizer-produced candidates with approve (apply weights) / reject actions.
-->
<template>
  <div class="research-page">
    <div class="page-header">
      <h2>自动研究</h2>
      <div class="header-right">
        <!-- 状态过滤：全部 / 待审批 / 已应用 / 已驳回 / 已审批 -->
        <select v-model="statusFilter" class="status-filter" @change="loadData">
          <option value="">全部</option>
          <option value="proposed">待审批</option>
          <option value="applied">已应用</option>
          <option value="approved">已审批</option>
          <option value="rejected">已驳回</option>
        </select>
        <button class="btn-refresh" @click="loadAll" :disabled="loading">
          {{ loading ? '加载中...' : '刷新' }}
        </button>
      </div>
    </div>

    <!-- 子页切换：待审批候选 / 战法库 / 设置 -->
    <div class="research-tabs">
      <button
        v-for="t in tabs"
        :key="t.key"
        :class="['tab', activeTab === t.key ? 'active' : '']"
        @click="activeTab = t.key"
      >
        {{ t.label }}
      </button>
    </div>

    <!-- ══════════ Tab 1: 待审批候选 ══════════ -->
    <template v-if="activeTab === 'candidates'">
    <!-- 研究处理进度：数据准备度 + 候选产出状态 -->
    <div class="progress-panel" v-if="progress">
      <div class="progress-title">研究处理进度</div>
      <div class="progress-grid">
        <!-- 数据准备度：近一年有行情的股票 / 全市场 -->
        <div class="progress-item">
          <div class="progress-label">数据准备度（近一年有行情 / 全市场）</div>
          <div class="progress-bar">
            <div class="progress-fill" :style="{ width: pct(progress.ready_pct) + '%' }"></div>
          </div>
          <div class="progress-meta">{{ progress.ready_stocks }} / {{ progress.stocks }} 只（{{ pct(progress.ready_pct) }}%）</div>
        </div>
        <!-- 日线覆盖 -->
        <div class="progress-item">
          <div class="progress-label">日线数据</div>
          <div class="progress-meta">{{ fmtRows(progress.daily_rows) }} 行</div>
        </div>
        <!-- 财务覆盖 -->
        <div class="progress-item">
          <div class="progress-label">财务指标</div>
          <div class="progress-meta">{{ fmtRows(progress.fin_rows) }} 行</div>
        </div>
        <!-- 候选产出 -->
        <div class="progress-item">
          <div class="progress-label">研究候选</div>
          <div class="progress-meta">
            <span class="meta-chip">{{ progress.candidates }} 条</span>
            <span class="meta-chip" v-if="progress.applied">已应用 {{ progress.applied }}</span>
            <span class="meta-chip" v-if="progress.proposed">待审批 {{ progress.proposed }}</span>
          </div>
        </div>
      </div>
    </div>
    </template>

    <!-- ══════════ Tab 2: 战法库 ══════════ -->
    <template v-else-if="activeTab === 'library'">
    <!-- 战法库：已应用的因子战法（启用/禁用/删除/重命名 + 效果监测） -->
    <div class="library-panel">
      <div class="library-header">
        <div class="library-title">战法库（已应用因子战法）</div>
        <button class="btn-refresh" @click="loadLibrary" :disabled="loadingLibrary">
          {{ loadingLibrary ? '加载中...' : '刷新' }}
        </button>
      </div>
      <!-- 运行中内置形态战法（引擎常驻，不经战法库文件）：一键历史回放（§用户反馈） -->
      <div class="builtin-patterns" style="margin:8px 0 12px">
        <div style="font-size:13px;color:var(--muted,#888);margin-bottom:6px">
          运行中内置形态战法（实盘常驻，点「回测」跑历史日K回放，结果进「回测」tab）：
        </div>
        <button v-for="b in builtinPatterns" :key="b.id" class="btn-backtest"
                style="margin-right:8px" @click="doLibraryBacktest(b)">
          回测·{{ b.name }}
        </button>
      </div>
      <div v-if="library.length === 0" class="empty">暂无已应用因子战法。审批通过的因子候选会自动加入战法库并注入 8a/8b 实盘。</div>
      <div v-else class="library-list">
        <div v-for="s in library" :key="s.id" class="library-card">
          <div class="lib-head">
            <span class="lib-name" v-if="!editingName[s.id]">{{ s.name }}</span>
            <input
              v-else
              v-model="nameDraft[s.id]"
              class="name-input"
              @keyup.enter="saveName(s)"
              @blur="saveName(s)"
            />
            <button class="btn-rename" @click="startRename(s)" v-if="canApprove && !editingName[s.id]">改名</button>
            <span :class="['tag', 'tag-kind', s.kind === 'pattern' ? 'kind-pattern' : 'kind-factor']">{{ s.kind === 'pattern' ? '形态' : '因子' }}</span>
            <span :class="['tag', s.enabled ? 'status-applied' : 'status-rejected']">{{ s.enabled ? '已启用' : '已停用' }}</span>
            <span class="lib-id">{{ s.id }}</span>
            <span class="lib-time">{{ s.applied_at }}</span>
          </div>
          <!-- 因子战法：因子方向 + 权重；形态战法：条件集 -->
          <div class="lib-factors">
            <template v-if="s.kind === 'pattern'">
              <span v-for="(c, i) in s.conds || []" :key="i" class="cond-chip">
                {{ condLabel(c) }}
              </span>
            </template>
            <template v-else>
              <span v-for="f in ruleFactors(s)" :key="f.id" class="factor-chip">
                <span :class="['dir-badge', f.dir < 0 ? 'short' : 'long']">{{ f.dir < 0 ? '看空' : '看多' }}</span>
                <span class="factor-name">{{ f.label }}</span>
                <span class="factor-id">{{ f.id }}</span>
              </span>
            </template>
          </div>
          <div class="lib-stats">
            <span class="stat">信号 <b>{{ s.signal_count }}</b></span>
            <span class="stat">胜 <b class="pos">{{ s.win }}</b></span>
            <span class="stat">负 <b class="neg">{{ s.loss }}</b></span>
            <span class="stat">累计前向收益 <b :class="s.cum_return >= 0 ? 'pos' : 'neg'">{{ fmtPct(s.cum_return) }}</b></span>
            <span class="stat">回测超额 <b :class="signClass(s.excess)">{{ fmt(s.excess) }}</b></span>
          </div>
          <!-- 规律验证：样本内外 IR / 反推超额 / 全样本 IR·IC / 全链路回测（候选表关联） -->
          <div class="lib-verify" v-if="s.kind === 'factor'">
            <div class="lib-verify-title">这条规律靠谱吗？（电脑验证过）</div>
            <div class="lib-verify-row">
              <span class="v-label">前瞻</span><span class="v-value">{{ s.horizon }} 个交易日</span>
              <span class="v-label">样本内 IR</span><span class="v-value">{{ fmt(parseReason(s,'样本内IR')) }}</span>
              <span class="v-label">样本外 IR</span><span class="v-value">{{ fmt(parseReason(s,'样本外IR')) }}</span>
              <span class="v-label">反推超额</span><span class="v-value">{{ fmtPct(parseReason(s,'反推超额')) }}</span>
              <span class="v-label">全样本 IR</span><span class="v-value">{{ fmt(s.ir) }}</span>
              <span class="v-label">全样本 IC</span><span class="v-value">{{ fmt(s.ic_mean) }}</span>
              <span class="v-label">全链路回测</span>
              <span class="v-value" :class="s.backtest_done ? (s.avg_excess >= 0 ? 'pos' : 'neg') : 'dim'">
                {{ s.backtest_done ? fmt(s.avg_excess) : '未测' }}
              </span>
            </div>
          </div>
          <div class="lib-actions" v-if="canApprove">
            <button class="btn-toggle" @click="toggleLibrary(s)">
              {{ s.enabled ? '停用' : '启用' }}
            </button>
            <!-- 阶段3.4 战法库回测入口：对该规则跑历史回放回测（结果进「回测」tab） -->
            <button class="btn-backtest" @click="doLibraryBacktest(s)">回测此战法</button>
            <button class="btn-reject" @click="removeLibrary(s)">删除</button>
          </div>
        </div>
      </div>
    </div>
    </template>

    <!-- ══════════ Tab 3: 设置 ══════════ -->
    <template v-else-if="activeTab === 'settings'">
    <div class="settings-panel">
      <div class="settings-title">研究调度设置</div>
      <div class="setting-row">
        <div class="setting-info">
          <div class="setting-label">全量回测全局开关</div>
          <div class="setting-desc">开启后，夜间自动研究在发现因子候选后会追加一次 B4 全链路回测（回填回测超额）；关闭则只做发现、不做回测，省时省 CPU。</div>
        </div>
        <label class="switch">
          <input type="checkbox" v-model="backtestEnabled" @change="saveBacktestToggle" />
          <span class="slider"></span>
        </label>
        <span class="setting-state">{{ backtestEnabled ? '已开启' : '已关闭' }}</span>
      </div>
      <div class="setting-hint">配置写入 rules.scheduler.nightly.backtest_enabled，quant-research 服务 30s 内热生效。</div>
    </div>
    </template>

    <!-- ══════════ Tab 4: 回测任务中心（进度查看 + 任务添加）══════════ -->
    <template v-else-if="activeTab === 'backtests'">
    <div class="bt-center">
      <!-- 任务添加：从待审批因子候选发起/重跑全量回测 -->
      <!-- Task add: launch / rerun a full backtest from proposed factor candidates -->
      <div class="bt-add">
        <div class="bt-add-title">发起 / 重跑全量回测</div>
        <!-- 阶段3.3 自定义回测参数：时长（起止日期）+ 选股数（top-k）+ 每日最小样本（min-stocks） -->
        <!-- Custom backtest params: range (start/end) plus picks per event and min daily sample -->
        <div class="bt-add-row bt-params-row">
          <label class="bt-param">开始 <input v-model="btStart" class="bt-input" placeholder="20230801" /></label>
          <label class="bt-param">结束 <input v-model="btEnd" class="bt-input" placeholder="留空=今天" /></label>
          <label class="bt-param">选股数 <input v-model="btTopK" class="bt-input bt-input-sm" placeholder="5" /></label>
          <label class="bt-param">最小样本 <input v-model="btMinStocks" class="bt-input bt-input-sm" placeholder="10" /></label>
        </div>
        <div class="bt-add-row">
          <select v-model="btPickId" class="bt-select" :disabled="btLoading">
            <option :value="0" disabled>选择待审批因子候选</option>
            <option v-for="c in btCandidates" :key="c.id" :value="c.id">
              #{{ c.id }} {{ c.kind === 'pattern' ? '形态战法' : '因子战法' }}（{{ c.kind === 'pattern' ? ('触发 ' + (c.triggers ?? '-')) : ('IC ' + fmt(c.ic_mean) + '，IR ' + fmt(c.ir)) }}）
            </option>
          </select>
          <button
            class="btn-backtest"
            :disabled="btPickId === 0 || backtestLoading[btPickId]"
            @click="doBacktestById(btPickId)"
          >
            {{ btPickId !== 0 && backtestLoading[btPickId] ? '回测中...' : '发起全量回测' }}
          </button>
          <button class="btn-refresh" @click="loadBacktests" :disabled="btLoading">
            {{ btLoading ? '加载中...' : '刷新列表' }}
          </button>
        </div>
        <div class="bt-add-hint">
          任务统一走研究队列：手动回测为高优先级，夜间自动研究为低优先级；高优先级到来会自动让路（被抢占任务断点续跑）。所有任务仅在盘后窗口执行——盘中提交会显示"排队中·盘后执行"。断点持久化，中断/重启后重跑只计算剩余事件；页面刷新后排队/运行中任务自动恢复轮询，可暂停/取消。
        </div>
      </div>

      <!-- 进度查看：全部回测任务（单候选 + 夜间全量，最新在前） -->
      <!-- Progress view: all backtest jobs (per-candidate + nightly, newest first) -->
      <div v-if="backtestJobs.length === 0" class="empty">
        暂无回测任务。选择上方候选发起全量回测，或等待夜间调度器产出。
      </div>
      <div v-else class="bt-list">
        <div v-for="j in backtestJobs" :key="(j.kind || 'candidate') + ':' + j.candidate_id" class="bt-card" :class="'bt-' + j.status">
          <div class="bt-head">
            <span :class="['tag', 'tag-kind', j.kind === 'nightly' ? '' : (j.kind === 'library' ? 'kind-pattern' : 'kind-factor')]">
              {{ j.kind === 'nightly' ? '夜间全量' : (j.kind === 'library' ? '战法库' : '单候选') }}
            </span>
            <span v-if="j.kind === 'candidate'" class="bt-cand">候选 #{{ j.candidate_id }}</span>
            <span v-else-if="j.kind === 'library'" class="bt-cand">{{ libraryJobLabel(j) }}</span>
            <span :class="['tag', 'bt-status', 'status-' + (j.status === 'done' ? 'applied' : (j.status === 'error' ? 'rejected' : 'proposed'))]">
              {{ btStatusLabel(j.status) }}
            </span>
            <span class="bt-time">{{ j.started_at || '' }}<template v-if="j.finished_at"> → {{ j.finished_at }}</template></span>
          </div>
          <!-- 进度条：运行中/已暂停显示实时进度；排队中显示 0% 占位（队列化改造：
               手动回测入队后等盘后窗口执行，期间保持可见与轮询） -->
          <div class="bt-progress" v-if="j.status === 'running' || j.status === 'paused' || j.status === 'queued'">
            <div class="bt-progress-bar">
              <div class="bt-progress-fill" :style="{ width: jobPct(j) }"></div>
            </div>
            <span class="bt-progress-label">{{ jobPct(j) }}</span>
          </div>
          <!-- 排队提示：盘后硬门控 + 前方排队计数（同级 FIFO，取同优先级 queued 在其之前的数量） -->
          <div class="bt-error" v-if="j.status === 'queued'" style="color: var(--muted, #888)">
            {{ queueHint(j) }}
          </div>
          <!-- 战法库回测结果：汇总报告文本（触发信号数/胜率/盈亏比等） -->
          <div class="bt-result bt-lib-result" v-if="j.status === 'done' && j.kind === 'library'">{{ j.result_text }}</div>
          <div class="bt-result" v-if="j.status === 'done' && j.kind !== 'library'">
            回测超额 <b :class="signClass(j.avg_excess)">{{ fmt(j.avg_excess) }}</b>
          </div>
          <div class="bt-error" v-if="j.status === 'error'">{{ j.error }}</div>
          <div class="bt-error" v-else-if="j.status === 'interrupted'">
            {{ j.error || '任务中断，可重新发起续跑（断点缓存仍有效，重跑只计算剩余事件）' }}
          </div>
          <div class="bt-actions" v-if="canApprove && (j.kind === 'candidate' || j.kind === 'library')">
            <!-- 阶段3.2 运行控制：运行中→暂停/取消；已暂停→继续/取消；已中断→续跑（断点续传）；其余→重新回测 -->
            <!-- Run controls: running→pause/cancel; paused→resume/cancel; interrupted→resume-run; else re-run -->
            <button
              v-if="j.status === 'running'"
              class="btn-backtest bt-ctl"
              @click="doPauseBacktest(ctrlId(j))"
            >暂停</button>
            <button
              v-if="j.status === 'paused'"
              class="btn-backtest bt-ctl"
              @click="doResumeBacktest(ctrlId(j))"
            >继续</button>
            <!-- 取消：运行中/已暂停/排队中均可取消（worker 对 queued 行直接置 cancelled 终态） -->
            <button
              v-if="j.status === 'running' || j.status === 'paused' || j.status === 'queued'"
              class="btn-backtest bt-ctl bt-danger"
              @click="doCancelBacktest(ctrlId(j))"
            >取消</button>
            <!-- 重新回测/续跑：候选与库规则/内置战法均支持（失败/中断后重跑，queued 不重复发起） -->
            <button
              v-else-if="j.kind === 'candidate' && j.status !== 'queued'"
              class="btn-backtest"
              :disabled="backtestLoading[j.candidate_id]"
              @click="doBacktestById(j.candidate_id)"
            >
              {{ j.status === 'interrupted' ? '续跑（断点续传）' : (backtestLoading[j.candidate_id] ? '回测中...' : '重新回测') }}
            </button>
            <button
              v-else-if="j.kind === 'library' && j.strategy_kind && j.status !== 'queued'"
              class="btn-backtest"
              @click="rerunLibrary(j)"
            >
              {{ j.status === 'interrupted' ? '重跑（断点续传）' : '重新回测' }}
            </button>
          </div>
        </div>
      </div>
    </div>
    </template>

    <!-- 待审批候选：空态 + 候选列表（Tab 1 内） -->
    <div v-if="activeTab === 'candidates' && noDB" class="empty">研究库未接入（需后端开启 B5 研究闭环）</div>
    <div v-else-if="activeTab === 'candidates' && candidates.length === 0" class="empty">暂无候选，先在命令行跑 research optimize 产出</div>

    <!-- 候选卡片列表 -->
    <div v-if="activeTab === 'candidates' && candidates.length > 0" class="candidate-list">
      <div v-for="c in candidates" :key="c.id" class="candidate-card">
        <div class="cand-header">
          <span class="cand-id">#{{ c.id }}</span>
          <span :class="['tag', 'tag-kind', 'kind-' + c.kind]">{{ kindLabel(c.kind) }}</span>
          <span :class="['tag', 'status-' + c.status]">{{ statusLabel(c.status) }}</span>
          <span class="cand-time">{{ c.created_at }}</span>
        </div>

        <!-- 因子战法候选：规则 + 验证 两块重点解释 -->
        <template v-if="c.kind === 'factor'">
          <div class="block-title">这条战法在做什么</div>
          <div class="factors-row">
            <div v-for="f in factorRule(c)" :key="f.id" class="factor-chip">
              <span :class="['dir-badge', f.dir < 0 ? 'short' : 'long']">{{ f.dir < 0 ? '看空' : '看多' }}</span>
              <span class="factor-name">{{ f.label }}</span>
              <span class="factor-id">{{ f.id }}</span>
              <span class="factor-weight">{{ f.weight.toFixed(2) }}权重</span>
            </div>
          </div>
          <div class="factor-desc">
            玩法：每天给所有股票按上面 {{ factorRule(c).length }} 个指标打分，分数最高的前一批会被标记为「值得买」，赌它们接下来 {{ c.horizon }} 个交易日能涨。
            <template v-if="factorRule(c).some(f => f.dir < 0)">
              注意：带「看空」的指标是反着用的——这项数值越高，反而越说明不该买。
            </template>
          </div>

          <!-- 验证：用大白话给结论 -->
          <div class="block-title">这条规律靠谱吗？（电脑验证过）</div>
          <div class="verify-plain">
            <div class="plain-summary">
              <span class="plain-badge" :class="verdict(c).ok ? 'good' : 'bad'">{{ verdict(c).ok ? '✅ 可以试试' : '⚠️ 建议别用' }}</span>
              <span class="plain-text">{{ verdict(c).text }}</span>
            </div>
            <div class="plain-line" v-for="(l, i) in plainLines(c)" :key="i">
              <span class="plain-num">{{ i + 1 }}.</span>
              <span class="plain-body">{{ l }}</span>
            </div>
          </div>

          <!-- 细节数据：折叠起来，想细看才展开 -->
          <details class="detail-block">
            <summary>想看具体数字？展开</summary>
            <div class="detail-row"><span class="d-label">样本内测试</span><span class="d-value">前一段历史回放：IR {{ fmt(parseReason(c,'样本内IR')) }}</span></div>
            <div class="detail-row"><span class="d-label">样本外测试</span><span class="d-value">另一段没用过的历史回放：IR {{ fmt(parseReason(c,'样本外IR')) }}</span></div>
            <div class="detail-row"><span class="d-label">反推超额</span><span class="d-value">高分股比全市场平均多赚 {{ fmtPct(parseReason(c,'反推超额')) }}</span></div>
            <div class="detail-row"><span class="d-label">全样本 IR</span><span class="d-value">{{ fmt(c.ir) }}（参考）</span></div>
            <div class="detail-row"><span class="d-label">全样本 IC</span><span class="d-value">{{ fmt(c.ic_mean) }}（参考）</span></div>
            <div class="detail-row"><span class="d-label">全链路回测</span><span class="d-value">{{ btTested(c) ? (c.backtest_result_text || fmt(c.avg_excess)) : '未测' }}</span></div>
          </details>
        </template>

        <!-- 其他类型候选（权重/形态/盘口）：保持原有简洁展示 -->
        <template v-else>
          <div class="metric-row">
            <div class="metric">
              <span class="metric-label">IR</span>
              <span :class="['metric-value', signClass(c.ir)]">{{ fmt(c.ir) }}</span>
            </div>
            <div class="metric">
              <span class="metric-label">IC</span>
              <span :class="['metric-value', signClass(c.ic_mean)]">{{ fmt(c.ic_mean) }}</span>
            </div>
            <div class="metric">
              <span class="metric-label">回测超额</span>
              <span :class="['metric-value', signClass(c.avg_excess)]">{{ fmt(c.avg_excess) }}</span>
            </div>
            <div class="metric">
              <span class="metric-label">前瞻天数</span>
              <span class="metric-value">{{ c.horizon }}</span>
            </div>
          </div>
          <div class="weights-row" v-if="weightList(c).length">
            <span class="weight-chip" v-for="w in weightList(c)" :key="w[0]">
              <span class="weight-fid">{{ w[0] }}</span>
              <span class="weight-val">{{ w[1].toFixed(3) }}</span>
            </span>
          </div>
        </template>

        <!-- 盘口托/压单明细（kind=depth） -->
        <div class="depth-block" v-if="c.kind === 'depth'">
          <div v-for="(s, code) in depthSummary(c)" :key="code" class="depth-stock">
            <span class="depth-code">{{ code }}</span>
            <span class="depth-touch">买1 {{ s.bid1 }} / 卖1 {{ s.ask1 }}</span>
            <span
              v-for="o in s.orders"
              :key="o.level + o.kind"
              :class="['order-chip', o.kind === 'support' ? 'support' : 'resistance']"
            >
              {{ o.kind === 'support' ? '托' : '压' }}单 档{{ o.level }}
              {{ o.price }} / {{ o.volume }}手 ({{ (o.share_pct * 100).toFixed(0) }}%)
            </span>
          </div>
        </div>
        <!-- 操作按钮：有 research_approve 权限且待审批时可审批 / 驳回；因子候选可单独全量回测 -->
        <div class="cand-actions" v-if="canApprove && c.status === 'proposed'">
          <button class="btn-approve" @click="doApprove(c)">审批并应用</button>
          <button class="btn-reject" @click="doReject(c)">驳回</button>
          <button
            v-if="c.kind === 'factor'"
            class="btn-backtest"
            :disabled="backtestLoading[c.id]"
            @click="doBacktest(c)"
          >
            {{ backtestLoading[c.id] ? '回测中...' : (c.avg_excess ? '重新回测' : '全量回测') }}
          </button>
          <!-- 回测进度条：job 创建即 0% 起步（后端落库 Progress="0%"），CLI 每 10% 推进，前端 5s 轮询刷新 -->
          <!-- Backtest progress bar: starts at 0% immediately (backend persists Progress="0%"), advances every
               10% from the CLI, refreshed by the 5s frontend polling -->
          <div v-if="backtestLoading[c.id]" class="bt-progress">
            <div class="bt-progress-bar">
              <div class="bt-progress-fill" :style="{ width: btPct(c.id) }"></div>
            </div>
            <span class="bt-progress-label">全链路回测 {{ backtestProgress[c.id] || '0%' }}</span>
          </div>
          <span v-if="backtestResult[c.id]" class="bt-result" :class="signClass(backtestResult[c.id])">
            回测超额 {{ fmt(backtestResult[c.id]) }}
          </span>
        </div>
        <!-- 无权限提示 -->
        <div class="no-perm" v-else-if="!canApprove && c.status === 'proposed'">
          无审批权限（需管理员授予 research_approve）
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onActivated, onUnmounted } from 'vue' // Vue 组合式 API：响应式引用/计算属性/生命周期钩子
// Vue Composition API: reactive ref / computed / lifecycle hooks
import * as api from '../api/index.js'           // 后端 API 调用封装（候选列表 / 审批 / 驳回）
// backend API wrapper (candidate list / approve / reject)

// ── 响应式状态 ──
// ── Reactive state ──
const loading = ref(false)      // 是否正在加载
// whether a load is in progress
const candidates = ref([])      // 候选列表
// candidate list
const noDB = ref(false)         // 研究库未接入
// research DB not wired up
const statusFilter = ref('')    // 状态过滤条件
// status filter
const progress = ref(null)      // 研究处理进度
// research processing progress
const library = ref([])         // 战法库（已应用因子战法）
// strategy library (applied factor strategies)
const loadingLibrary = ref(false)
// library loading flag
const backtestLoading = ref({}) // 候选 id → 是否回测中
// candidate id → backtest in progress
const backtestState = ref({})   // 候选 id → running/done/error
// candidate id → backtest state
const backtestResult = ref({})  // 候选 id → 回测超额结果
// candidate id → backtest excess result
const backtestProgress = ref({}) // 候选 id → 回测进度（如 "45%"）
// candidate id → backtest progress (e.g. "45%")

// ── 回测任务中心（Tab 4：进度查看 + 任务添加）──
// Backtest task center (Tab 4: progress view + task launch)
const backtestJobs = ref([]) // 全部回测任务（单候选 + 夜间，最新在前）
// all backtest jobs (per-candidate + nightly, newest first)
const btLoading = ref(false)  // 回测任务列表加载中
// backtest job list loading
const btPickId = ref(0)       // 任务添加：选中的候选 ID
// 阶段3.3 自定义回测参数（空值=CLI 默认）
const btStart = ref('')       // 回测开始日期 YYYYMMDD
const btEnd = ref('')         // 回测结束日期（留空=今天）
const btTopK = ref('')        // 每事件选股数
const btMinStocks = ref('')   // 每日最小样本
// task-add: selected candidate ID
const backtestPollers = {}    // 候选 id → 轮询 interval（页面刷新/切换后防重复轮询）
// candidate id → polling interval (deduped across refreshes / tab switches)

// 子页 tab（待审批候选 / 战法库 / 设置 / 回测任务中心）
// Sub-tabs: proposed candidates / strategy library / settings / backtest task center
const tabs = [
  { key: 'candidates', label: '待审批候选' },
  { key: 'library', label: '战法库' },
  { key: 'backtests', label: '回测' },
  { key: 'settings', label: '设置' },
]
const activeTab = ref('candidates')
// active sub-tab
const editingName = ref({}) // 战法 id → 是否在改名
const nameDraft = ref({})   // 战法 id → 改名草稿
const backtestEnabled = ref(false) // 全量回测全局开关（保存到 rules.scheduler.nightly.backtest_enabled）

/** 百分比显示（0~100 取整） */
/** Percentage display (0~100, rounded) */
function pct(v) {
  if (v === null || v === undefined || isNaN(v)) return 0
  return Math.min(100, Math.round(v * 100))
}

/** 行数格式化：千分位 */
/** Row count formatting with thousands separators */
function fmtRows(v) {
  if (v === null || v === undefined || isNaN(v)) return '-'
  return Number(v).toLocaleString('zh-CN')
}

/** 加载研究处理进度 */
/** Load the research processing progress */
async function loadProgress() {
  try {
    const p = await api.fetchResearchProgress()
    if (p) progress.value = p
  } catch (e) {
    // 进度接口不可用（研究库未接入等）静默降级，不影响候选列表
    console.error('Research 进度加载失败', e)
  }
}

/** 刷新全部（进度 + 候选） */
/** Refresh both progress and candidate list */
async function loadAll() {
  loading.value = true
  await loadProgress()
  await loadData()
  loading.value = false
}

/** 是否拥有研究审批权限（research_approve；admin 隐式拥有） */
/** Whether the user may approve/reject research candidates (research_approve; admin implies all) */
const canApprove = api.hasPerm('research_approve')

/** 状态显示文本 */
/** Human-readable status label */
function statusLabel(s) {
  const m = { proposed: '待审批', approved: '已审批', applied: '已应用', rejected: '已驳回' }
  return m[s] || s
}

/** 格式化指标：保留 4 位，空值显示 '-' */
/** Format a metric to 4 decimals; '-' when empty */
function fmt(v) {
  if (v === null || v === undefined || isNaN(v)) return '-'
  return Number(v).toFixed(4)
}

/** 指标正负样式类：正数绿、负数红 */
/** Sign-based style class: positive green, negative red */
function signClass(v) {
  if (v === null || v === undefined || isNaN(v)) return ''
  return Number(v) >= 0 ? 'pos' : 'neg'
}

/** 解析权重 JSON 为排序后的 [factorID, weight] 数组 */
/** Parse the weights JSON into a sorted [factorID, weight] array */
function weightList(c) {
  try {
    const w = JSON.parse(c.weights || '{}')
    // E6 因子候选：weights 为复合结构 {"weights":{...},"directions":{...},"buy_threshold":N}，
    // 只取其中数值权重子对象，避免把 directions/buy_threshold 等非数值项丢进渲染导致 toFixed 报错。
    // English: E6 factor candidates store a composite {"weights":{...},"directions":{...},"buy_threshold":N};
    // extract only the numeric weights sub-object so non-numeric entries never reach toFixed() in render.
    const wm = (w && typeof w === 'object' && w.weights && typeof w.weights === 'object' && !Array.isArray(w.weights))
      ? w.weights
      : w
    if (!wm || typeof wm !== 'object') return []
    const entries = Object.entries(wm).filter(([, v]) => typeof v === 'number' && isFinite(v))
    return entries.sort((a, b) => b[1] - a[1])
  } catch (_) {
    return []
  }
}

/** 解析盘口托/压单候选的 weights JSON（code → {orders, bid1, ask1}） */
/** Parse depth-candidate weights JSON (code → {orders, bid1, ask1}) */
function depthSummary(c) {
  try {
    return JSON.parse(c.weights || '{}')
  } catch (_) {
    return {}
  }
}

// ── 因子战法候选的可解释展示 ──
// Factor-strategy candidate human-readable rendering

/** 候选类型中文名 */
/** Human-readable candidate kind label */
function kindLabel(k) {
  const m = { factor: '因子战法', pattern: '形态战法', weights: '权重优化', depth: '盘口扫描' }
  return m[k] || k
}

// 因子元数据缓存（从后端 /api/research/factors 拉取，ID → { name, cat, desc }）
// Factor metadata cache, fetched from /api/research/factors (ID → { name, cat, desc })
const factorMeta = {}

/** 拉取并缓存因子元数据（失败静默降级，仍可用 ID 兜底显示） */
/** Fetch and cache factor metadata; silently degrade to ID on failure */
async function loadFactorMeta() {
  try {
    const res = await api.fetchResearchFactors()
    if (res && Array.isArray(res.factors)) {
      for (const f of res.factors) {
        if (f && f.id) factorMeta[f.id] = f
      }
    }
  } catch (_) {
    // 后端未提供时保留空映射，前端以 ID 兜底
  }
}

/** 因子 ID → 中文展示名（优先后端元数据，缺失回退 ID） */
/** Factor ID → display name (backend metadata first, fallback to ID) */
function factorName(id) {
  const m = factorMeta[id]
  return (m && m.name) ? m.name : id
}

/** 取因子方向（factor 候选的 weights 是复合结构 {"directions":{...},"weights":{...}}） */
/** Read factor directions from a factor candidate's composite weights */
function factorDirs(c) {
  try {
    const w = JSON.parse(c.weights || '{}')
    if (w && typeof w === 'object' && w.directions && typeof w.directions === 'object') {
      return w.directions
    }
  } catch (_) {}
  return {}
}

/** 因子规则：合并方向 + 权重 + 中文名，按权重降序 */
/** Factor rule rows: direction + weight + Chinese name, sorted by weight desc */
function factorRule(c) {
  const dirs = factorDirs(c)
  const wm = {}
  try {
    const w = JSON.parse(c.weights || '{}')
    const weightsObj = (w && w.weights && typeof w.weights === 'object' && !Array.isArray(w.weights)) ? w.weights : w
    Object.assign(wm, weightsObj)
  } catch (_) {}
  return Object.entries(wm)
    .filter(([, v]) => typeof v === 'number' && isFinite(v))
    .map(([id, weight]) => ({
      id,
      label: factorName(id),
      weight,
      dir: (typeof dirs[id] === 'number' && dirs[id] < 0) ? -1 : 1,
    }))
    .sort((a, b) => b.weight - a.weight)
}

/** 从 reason 字符串提取数值（样本内IR / 样本外IR / 反推超额） */
/** Extract a numeric value from the reason string */
function parseReason(c, key) {
  const reason = c.reason || ''
  const pats = {
    '样本内IR': /样本内IR=(-?\d+\.?\d*)/,
    '样本外IR': /样本外IR=(-?\d+\.?\d*)/,
    '反推超额': /反推超额=(-?\d+\.?\d*)/,
  }
  const m = reason.match(pats[key])
  return m ? parseFloat(m[1]) : null
}

/** 格式化百分数（反推超额是小数，如 0.1637 → +16.4%） */
/** Format a ratio as a percentage (0.1637 → +16.4%) */
function fmtPct(v) {
  if (v === null || v === undefined || isNaN(v)) return '-'
  const s = (v * 100).toFixed(1)
  return (v >= 0 ? '+' : '') + s + '%'
}

/** 总体结论：这条规律能不能用（大白话） */
/** Overall verdict: should this rule be used (plain words) */
function verdict(c) {
  const insample = parseReason(c, '样本内IR')
  const outsample = parseReason(c, '样本外IR')
  const gen = parseReason(c, '反推超额')
  const thr = 0.3 // 与 research --min-ir 默认一致
  const passed = (insample !== null ? insample >= thr : true) &&
                 (outsample !== null ? outsample >= thr : true) &&
                 (gen !== null ? gen > 0 : true)
  if (passed) {
    return { ok: true, text: '电脑用两段互不相干的历史行情分别验证过，这条规律都能跑赢，不是碰运气。' }
  }
  return { ok: false, text: '这条规律在验证中没站稳，不建议直接拿来实盘。' }
}

/** 三条大白话验证结论 */
/** Three plain-language verification conclusions */
function plainLines(c) {
  const insample = parseReason(c, '样本内IR')
  const outsample = parseReason(c, '样本外IR')
  const gen = parseReason(c, '反推超额')
  const lines = []
  if (insample !== null) {
    lines.push('先拿前半段历史行情回放：这套打分的选股效果明显（稳定度 ' + fmt(insample) + '，越高越靠谱）。')
  }
  if (outsample !== null) {
    lines.push('再拿一段完全没参与挑规律的行情回放：效果仍然明显（稳定度 ' + fmt(outsample) + '）。这一步是防止规律只对老数据灵、换市场就失灵。')
  }
  if (gen !== null) {
    lines.push('最后对比「按这套规律选出的股票」和「随便买」：选的比平均多赚 ' + fmtPct(gen) + '，说明规律确实挑得出好股票。')
  }
  if (lines.length === 0) {
    lines.push(/通过护栏/.test(c.reason || '') ? '电脑验证通过，这条规律可以试试。' : '这条规律未通过验证。')
  }
  return lines
}

/** 回测是否真的做过：优先看后端通用证据 backtest_done（factor=B4 回填 /
 *  pattern=回放任务 done），无该字段时回退旧口径 avg_excess≠0 */
function btTested(c) {
  if (typeof c.backtest_done === 'boolean') return c.backtest_done
  return c.avg_excess !== 0
}

/** 回测进度百分比（进度字符串 "45%" → 用于进度条宽度） */
/** backtest progress percent ("45%" → progress-bar width) */
function btPct(id) {
  const p = backtestProgress.value[id]
  if (!p) return '0%'
  const n = parseInt(p, 10)
  return (isNaN(n) ? 0 : Math.max(0, Math.min(100, n))) + '%'
}

/** 从后端加载候选列表 */
/** Load the candidate list from the backend */
async function loadData() {
  loading.value = true
  try {
    const res = await api.fetchResearchCandidates(statusFilter.value || '')
    if (res && Array.isArray(res.candidates)) {
      candidates.value = res.candidates
      noDB.value = false
    }
  } catch (e) {
    if (e && e.message && e.message.indexOf('研究库未接入') >= 0) {
      noDB.value = true
      candidates.value = []
    } else {
      console.error('Research 加载失败', e)
    }
  } finally {
    loading.value = false
  }
}

/** 审批通过并应用候选 */
/** Approve a candidate and apply its weights */
async function doApprove(c) {
  try {
    await api.approveResearchCandidate(c.id)
    c.status = 'applied'
    alert('候选 #' + c.id + ' 已审批并应用')
  } catch (e) {
    alert('审批失败: ' + (e.message || e))
  }
}

/** 驳回候选 */
/** Reject a candidate */
async function doReject(c) {
  try {
    await api.rejectResearchCandidate(c.id)
    c.status = 'rejected'
    alert('候选 #' + c.id + ' 已驳回')
  } catch (e) {
    alert('驳回失败: ' + (e.message || e))
  }
}

// ── 战法库（已应用因子战法管理 + 效果监测）──
// Strategy library: management + effectiveness monitoring

/** 解析战法库条目的因子规则（方向 + 中文名） */
/** Parse a library entry's factor rules (direction + Chinese name) */
function ruleFactors(s) {
  const dirs = s.directions || {}
  const wm = s.weights || {}
  return Object.entries(wm)
    .filter(([, v]) => typeof v === 'number' && isFinite(v))
    .map(([id, weight]) => ({
      id,
      label: factorName(id),
      weight,
      dir: (typeof dirs[id] === 'number' && dirs[id] < 0) ? -1 : 1,
    }))
    .sort((a, b) => b.weight - a.weight)
}

/** 形态条件显示文案：因子 ∈ [min, max) */
/** Pattern condition label: factor ∈ [min, max) */
function condLabel(c) {
  const name = factorName(c.factor || '')
  const min = (c.min !== undefined && c.min !== null) ? c.min : '-∞'
  const max = (c.max !== undefined && c.max !== null) ? c.max : '+∞'
  return name + ' ∈ [' + min + ', ' + max + ')'
}

/** 加载战法库 */
/** Load the strategy library */
async function loadLibrary() {
  loadingLibrary.value = true
  try {
    const res = await api.fetchResearchLibrary()
    if (res && Array.isArray(res.library)) {
      library.value = res.library
    }
  } catch (e) {
    console.error('战法库加载失败', e)
  } finally {
    loadingLibrary.value = false
  }
}

/** 启用/禁用战法库某条 */
/** Enable/disable a library strategy */
async function toggleLibrary(s) {
  try {
    await api.setResearchLibraryEnabled(s.id, !s.enabled)
    s.enabled = !s.enabled
    alert('战法 ' + s.name + (s.enabled ? ' 已启用（已注入 8a/8b 实盘）' : ' 已停用'))
  } catch (e) {
    alert('操作失败: ' + (e.message || e))
  }
}

/** 删除战法库某条 */
/** Delete a library strategy */
async function removeLibrary(s) {
  if (!confirm('确定删除战法 ' + s.name + ' ？删除后不再注入 8a/8b 实盘。')) return
  try {
    await api.deleteResearchLibrary(s.id)
    library.value = library.value.filter(x => x.id !== s.id)
    alert('战法 ' + s.name + ' 已删除')
  } catch (e) {
    alert('删除失败: ' + (e.message || e))
  }
}

/** 开始改名：进入编辑态 */
/** Start rename: enter edit mode */
function startRename(s) {
  editingName.value = { ...editingName.value, [s.id]: true }
  nameDraft.value = { ...nameDraft.value, [s.id]: s.name }
}

/** 保存改名 */
/** Save the rename */
async function saveName(s) {
  const name = (nameDraft.value[s.id] || '').trim()
  editingName.value = { ...editingName.value, [s.id]: false }
  if (!name || name === s.name) return
  try {
    await api.renameResearchLibrary(s.id, name)
    s.name = name
    alert('战法已重命名为 ' + name)
  } catch (e) {
    alert('改名失败: ' + (e.message || e))
  }
}

/** 加载全量回测全局开关 */
/** Load the global full-backtest toggle */
async function loadBacktestToggle() {
  try {
    const res = await api.fetchBacktestToggle()
    if (res && typeof res.enabled === 'boolean') backtestEnabled.value = res.enabled
  } catch (e) {
    console.error('加载回测开关失败', e)
  }
}

/** 保存全量回测全局开关 */
/** Save the global full-backtest toggle */
async function saveBacktestToggle() {
  try {
    await api.setBacktestToggle(backtestEnabled.value)
    alert('全量回测全局开关已' + (backtestEnabled.value ? '开启' : '关闭'))
  } catch (e) {
    alert('保存失败: ' + (e.message || e))
    backtestEnabled.value = !backtestEnabled.value
  }
}

// ── 单条候选全量回测（异步）+ 回测任务中心轮询 ──
// Per-candidate full backtest (async) + task-center polling

/** 对指定候选触发全量回测并轮询进度 */
/** Trigger a full backtest on a candidate and poll its progress */
async function doBacktest(c) {
  if (backtestPollers[c.id]) return // 已在轮询，防止重复启动（防重入）
  backtestLoading.value = { ...backtestLoading.value, [c.id]: true }
  backtestState.value = { ...backtestState.value, [c.id]: 'running' }
  try {
    // 阶段3.3：携带自定义参数（时长 + 选股数 + 最小样本；空值由后端保持 CLI 默认）
    await api.backtestResearchCandidate(c.id, {
      start: btStart.value.trim(),
      end: btEnd.value.trim(),
      top_k: parseInt(btTopK.value, 10) || 0,
      min_stocks: parseInt(btMinStocks.value, 10) || 0,
    })
  } catch (e) {
    backtestLoading.value = { ...backtestLoading.value, [c.id]: false }
    backtestState.value = { ...backtestState.value, [c.id]: 'error' }
    alert('回测启动失败: ' + (e.message || e))
    return
  }
  pollBacktest(c)
}

/** 阶段3.2 运行控制：取消（kill+interrupted，断点缓存可续跑） */
async function doCancelBacktest(id) {
  if (!confirm('取消候选 #' + id + ' 的回测？（已算完的事件保留缓存，续跑只算剩余）')) return
  try {
    await api.cancelBacktest(id)
    loadBacktests()
  } catch (e) { alert('取消失败: ' + (e.message || e)) }
}
/** 阶段3.2 运行控制：暂停（SIGSTOP） */
async function doPauseBacktest(id) {
  try {
    await api.pauseBacktest(id)
    loadBacktests()
  } catch (e) { alert('暂停失败: ' + (e.message || e)) }
}
/** 阶段3.2 运行控制：继续（SIGCONT） */
async function doResumeBacktest(id) {
  try {
    await api.resumeBacktest(id)
    loadBacktests()
  } catch (e) { alert('恢复失败: ' + (e.message || e)) }
}

// ── 阶段3.4 战法库回测入口 ──
let libPollTimer = null

// 运行中内置形态战法（与后端 builtinStrategies 的序号映射一致）
const builtinPatterns = [
  { id: 'double_bump', name: '双响炮' },
  { id: 'dragon', name: '龙头' },
  { id: 'dragon_return', name: '龙回头' },
  { id: 'n_shape', name: 'N形（日K近似）' },
]
/** 排队提示文案：盘中提交→等待盘后窗口；窗口内排队→显示前方还有几个同类任务 */
function queueHint(j) {
  const ahead = backtestJobs.value.filter(x =>
    x.status === 'running' ||
    (x.status === 'queued' && (x.id || 0) < (j.id || 0))
  ).length
  return ahead > 0
    ? `排队中：前方还有 ${ahead} 个任务，将按优先级依次执行`
    : '已加入队列，将在非交易时段自动执行（交易日盘后起 / 非交易日全天；绝不进入盘中）'
}

/** 库回放任务显示名：内置战法序号→名称；ref 0=夜间全量回放（库规则+四大内置） */
function libraryJobLabel(j) {
  if (j.candidate_id === 0) return '夜间全量回放'
  return builtinLabel(j.candidate_id)
}

/** 失败/中断的库回放任务重跑：内置战法按名直发；库规则按 strategy_kind 重建 fac_/pat_ ID */
async function rerunLibrary(j) {
  const sk = j.strategy_kind
  const id = ['double_bump', 'dragon', 'dragon_return', 'n_shape'].includes(sk)
    ? sk
    : (sk === 'factor' ? 'fac_' : 'pat_') + j.candidate_id
  try {
    await api.backtestLibraryRule(id, {})
    startLibPoll()
    await loadBacktests()
  } catch (e) { alert('发起失败: ' + (e.message || e)) }
}

/** 内置战法的回测任务序号 → 显示名（901-904 与后端约定一致） */
function builtinLabel(num) {
  const m = { 901: '双响炮', 902: '龙头', 903: '龙回头', 904: 'N形' }
  return m[num] || ('规则 ' + num)
}
/** 对战法库一条规则发起历史回放回测（异步），完成后在「回测」tab 展示汇总报告 */
async function doLibraryBacktest(s) {
  if (!confirm(`回测战法「${s.name || s.id}」？（历史日K回放，结果进「回测」tab）`)) return
  try {
    await api.backtestLibraryRule(s.id, { start: btStart.value.trim(), end: btEnd.value.trim() })
    activeTab.value = 'backtests'
    await loadBacktests()
    startLibPoll()
  } catch (e) { alert('发起失败: ' + (e.message || e)) }
}
/** 战法库回测轻量轮询：有 running/paused 的 library 任务时每 5s 刷新任务列表，全部结束即停 */
function startLibPoll() {
  if (libPollTimer) return
  libPollTimer = setInterval(async () => {
    await loadBacktests()
    // busy 判定含 queued：排队中的战法库回测也要持续刷新列表直到真正执行完毕
    const busy = backtestJobs.value.some(j => j.kind === 'library' && (j.status === 'running' || j.status === 'paused' || j.status === 'queued'))
    if (!busy) { clearInterval(libPollTimer); libPollTimer = null }
  }, 5000)
}

/** 轮询单个候选的回测任务状态（全量回测可能耗时较长；interval 唯一，防重复） */
/** Poll a single candidate's backtest job (a full backtest can be slow; one interval per candidate) */
function pollBacktest(c) {
  const id = c.id
  if (backtestPollers[id]) return
  backtestPollers[id] = setInterval(async () => {
    try {
      const j = await api.fetchBacktestStatus(id)
      // 回测进度实时更新（后端子进程输出逐行解析出"回测进度 xx%"）
      if (j.progress) {
        backtestProgress.value = { ...backtestProgress.value, [id]: j.progress }
        // 任务列表同步刷新，回测 tab 进度条跟着走
        syncJobIntoList(j)
      }
      if (j.status === 'done') {
        clearPoll(id)
        backtestLoading.value = { ...backtestLoading.value, [id]: false }
        backtestState.value = { ...backtestState.value, [id]: 'done' }
        backtestProgress.value = { ...backtestProgress.value, [id]: null }
        backtestResult.value = { ...backtestResult.value, [id]: j.avg_excess }
        c.avg_excess = j.avg_excess
        syncJobIntoList({ status: 'done', candidate_id: id, progress: '100%', avg_excess: j.avg_excess })
        alert('候选 #' + id + ' 回测完成，回测超额 ' + (j.avg_excess !== undefined ? (j.avg_excess * 100).toFixed(2) + '%' : '0%'))
        loadLibrary()
      } else if (j.status === 'error') {
        clearPoll(id)
        backtestLoading.value = { ...backtestLoading.value, [id]: false }
        backtestState.value = { ...backtestState.value, [id]: 'error' }
        backtestProgress.value = { ...backtestProgress.value, [id]: null }
        syncJobIntoList({ status: 'error', candidate_id: id, progress: '100%', error: j.error })
        alert('候选 #' + id + ' 回测失败: ' + (j.error || ''))
      }
    } catch (e) {
      // 轮询临时失败，继续
    }
  }, 5000)
}

/** 清除某候选的轮询（interval 去重 + 卸载清理用） */
/** Stop a candidate's polling (used for dedup and unmount cleanup) */
function clearPoll(id) {
  if (backtestPollers[id]) {
    clearInterval(backtestPollers[id])
    delete backtestPollers[id]
  }
}

/** 页面刷新/重新挂载后恢复运行中任务的轮询（刷新不再丢进度） */
/** Restore polling for running jobs after a page refresh / remount (progress survives refreshes) */
async function restoreRunningBacktests() {
  try {
    const res = await api.fetchRunningBacktests()
    const jobs = (res && res.jobs) || []
    for (const j of jobs) {
      // queued 同样恢复轮询：任务在盘后被 worker 拉起时，前端能自动切到运行态并接续进度条。
      // 仅候选任务走逐 id 轮询+弹窗——库回放/内置战法任务由列表轮询(startLibPoll/loadBacktests)
      // 展示进度，避免合成 id=0 的"候选 #0"假轮询与误导性失败弹窗。
      if (j.kind !== 'candidate') continue
      if (j.status !== 'running' && j.status !== 'queued') continue
      const cand = candidates.value.find(x => x.id === j.candidate_id)
      const c = cand || { id: j.candidate_id, kind: 'factor' }
      backtestLoading.value = { ...backtestLoading.value, [c.id]: true }
      backtestState.value = { ...backtestState.value, [c.id]: 'running' }
      if (j.progress) backtestProgress.value = { ...backtestProgress.value, [c.id]: j.progress }
      pollBacktest(c)
    }
  } catch (e) {
    // running 接口不可用（研究库未接入）静默降级
    console.error('恢复运行中回测任务失败', e)
  }
}

/** 加载全部回测任务列表（回测 tab 进度查看） */
/** Load all backtest jobs (backtest-tab progress view) */
async function loadBacktests() {
  btLoading.value = true
  try {
    const res = await api.fetchAllBacktests()
    if (res && Array.isArray(res.jobs)) {
      // 阶段3.1 去重防御：按 kind+candidate_id 唯一（杜绝历史脏数据/合并缺口导致的重复卡片）
      const seen = new Set()
      backtestJobs.value = res.jobs.filter(j => {
        const k = (j.kind || 'candidate') + ':' + j.candidate_id
        if (seen.has(k)) return false
        seen.add(k)
        return true
      })
    }
  } catch (e) {
    console.error('加载回测任务失败', e)
  } finally {
    btLoading.value = false
  }
}

/** 把单候选任务状态同步进任务列表（轮询期间回测 tab 进度条实时更新） */
/** Merge a per-candidate job update into the task list (live progress on the backtest tab) */
function syncJobIntoList(j) {
  // kind 感知合并（§修复轮询克隆）：后端状态接口已回带 kind；旧调用方未带时按
  // 'candidate' 回退。找不到同键行才 unshift——此前 kind 缺失导致每 5s 克隆一行。
  const jobs = backtestJobs.value.slice()
  const jk = j.kind || 'candidate'
  const idx = jobs.findIndex(x => (x.kind || 'candidate') === jk && x.candidate_id === j.candidate_id)
  const merged = { ...(idx >= 0 ? jobs[idx] : { id: j.task_id }), ...j, kind: jk }
  if (idx >= 0) jobs[idx] = merged
  else jobs.unshift(merged)
  backtestJobs.value = jobs
}

/** 任务添加：按 ID 从候选列表找到对象并发起回测（回测 tab 的"重新回测"按钮复用） */
/** Task-add: find the candidate by ID and launch its backtest (reused by the tab's "重新回测") */
function doBacktestById(id) {
  const c = candidates.value.find(x => x.id === id)
  if (c) doBacktest(c)
  else alert('候选 #' + id + ' 不存在（请先刷新候选列表）')
}

/** 任务状态中文标签（阶段3.2 增 paused；队列化改造增 queued/preempted）
 *  queued：任务已入队但未到盘后执行窗口（盘后硬门控），或排在其他任务之后等待；
 *  preempted：被高优先级任务抢占/会话终止——后端对外统一映射为 interrupted，这里仅防御。
 *  English: status labels — 'queued' means enqueued but waiting for the after-hours window;
 *  'preempted' is defensively mapped (the backend already exposes it as 'interrupted'). */
function btStatusLabel(s) {
  const m = {
    running: '运行中', paused: '已暂停', done: '已完成', error: '失败',
    interrupted: '已中断', queued: '排队中·盘后执行', preempted: '已中断',
  }
  return m[s] || s
}
/** 运行控制接口的任务键：战法库任务用合成键（1e9+规则序号，与后端 libraryJobKey 对齐） */
function ctrlId(j) {
  return j.kind === 'library' ? 1000000000 + j.candidate_id : j.candidate_id
}

/** 任务进度条宽度（任务对象版本，兼容无 progress 字段） */
/** Job progress-bar width (job-object variant; tolerates a missing progress field) */
function jobPct(j) {
  const p = j.progress || '0%'
  const n = parseInt(p, 10)
  return (isNaN(n) ? 0 : Math.max(0, Math.min(100, n))) + '%'
}

/** 可发起回测的候选：待审批的因子战法候选（回测 tab 任务添加下拉） */
/** Candidates available for backtest: proposed factor candidates (task-add dropdown) */
// 可发起回测的候选：待审批的因子/形态候选（§8.6-B 形态走战法库回放引擎，端点相同）
const btCandidates = computed(() =>
  candidates.value.filter(c => (c.kind === 'factor' || c.kind === 'pattern') && c.status === 'proposed')
)

// 挂载时加载一次；KeepAlive 缓存激活时刷新（切换 tab 回来自动同步最新候选）
// Load once on mount; refresh on KeepAlive reactivation so switching tabs syncs the latest candidates
// 刷新/重新激活时恢复运行中回测任务的轮询 + 加载任务列表（回测 tab）
// English: on mount/activation also restore polling for running backtest jobs and load the job list
onMounted(() => { loadFactorMeta(); loadAll(); loadLibrary(); loadBacktestToggle(); startPolling(); loadBacktests(); restoreRunningBacktests() })
onActivated(() => { loadAll(); loadLibrary(); loadBacktestToggle(); startPolling(); loadBacktests(); restoreRunningBacktests() })
onUnmounted(() => { stopPolling(); Object.keys(backtestPollers).forEach(clearPoll) })

// 定时轮询研究进度（30s）：dataload 期间进度条实时推进
// Poll the research progress every 30s so the data-loading progress bar stays fresh
let pollTimer = null
function startPolling() {
  if (pollTimer) return
  pollTimer = setInterval(loadProgress, 30000)
}
function stopPolling() {
  if (pollTimer) { clearInterval(pollTimer); pollTimer = null }
}
</script>

<style scoped>
.research-page { max-width: 960px; margin: 0 auto; }
.page-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 14px; }
.page-header h2 { font-size: 18px; }
.header-right { display: flex; align-items: center; gap: 10px; }
.status-filter {
  padding: 6px 10px; border-radius: 6px; border: 1px solid #333;
  background: #0f0f23; color: #e0e0e0; font-size: 14px; outline: none;
}
.btn-refresh { padding: 6px 14px; border-radius: 6px; border: 1px solid #FF4D4F; background: transparent; color: #FF4D4F; font-size: 14px; cursor: pointer; }
.btn-refresh:disabled { opacity: 0.5; }
.btn-refresh:hover { background: rgba(255,77,79,0.1); }

.empty { text-align: center; padding: 40px; color: #666; font-size: 14px; }

.progress-panel { background: #1a1a2e; border: 1px solid #2a2a3e; border-radius: 8px; padding: 12px 14px; margin-bottom: 14px; }
.progress-title { font-size: 13px; font-weight: 600; color: #e0e0e0; margin-bottom: 10px; }
.progress-grid { display: grid; grid-template-columns: 2fr 1fr 1fr 1fr; gap: 14px; }
.progress-item { display: flex; flex-direction: column; gap: 4px; }
.progress-label { font-size: 12px; color: #888; }
.progress-bar { height: 8px; border-radius: 4px; background: #2a2a3e; overflow: hidden; }
.progress-fill { height: 100%; border-radius: 4px; background: linear-gradient(90deg, #4caf50, #64b5f6); transition: width 0.6s ease; }
.progress-meta { font-size: 12px; color: #aaa; display: flex; flex-wrap: wrap; gap: 6px; }
.meta-chip { padding: 1px 8px; border-radius: 4px; background: #2a2a3e; color: #64b5f6; white-space: nowrap; }

@media (max-width: 768px) {
  .progress-grid { grid-template-columns: 1fr 1fr; }
}

/* 战法库（已应用因子战法） */
.library-panel { background: #16162a; border: 1px solid #2a2a3e; border-radius: 8px; padding: 12px 14px; margin-bottom: 14px; }
.library-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 10px; }
.library-title { font-size: 13px; font-weight: 600; color: #e0e0e0; }
.library-list { display: flex; flex-direction: column; gap: 10px; }
.library-card { background: #1a1a2e; border-radius: 8px; padding: 10px 12px; border: 1px solid #2a2a3e; }
.lib-head { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
.lib-name { font-size: 13px; font-weight: 600; color: #e0e0e0; }
.lib-id { font-size: 11px; color: #777; }
.lib-time { font-size: 11px; color: #777; margin-left: auto; }
.lib-factors { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 8px; }
.lib-stats { display: flex; flex-wrap: wrap; gap: 14px; font-size: 12px; color: #aaa; margin-bottom: 8px; }
 .lib-stats .stat b { font-weight: 600; }
 .lib-stats .stat b.pos { color: #4caf50; }
 .lib-stats .stat b.neg { color: #FF4D4F; }
 .lib-verify { background: #141428; border: 1px solid #2a2a3e; border-radius: 6px; padding: 8px 10px; margin-bottom: 8px; }
 .lib-verify-title { font-size: 12px; font-weight: 600; color: #e0e0e0; margin-bottom: 6px; }
 .lib-verify-row { display: flex; flex-wrap: wrap; gap: 8px 16px; font-size: 12px; color: #aaa; align-items: baseline; }
 .lib-verify-row .v-label { color: #777; }
 .lib-verify-row .v-value { font-weight: 600; color: #e0e0e0; }
 .lib-verify-row .v-value.pos { color: #4caf50; }
 .lib-verify-row .v-value.neg { color: #FF4D4F; }
 .lib-verify-row .v-value.dim { color: #777; font-weight: 400; }
.lib-actions { display: flex; gap: 10px; }
.btn-toggle { padding: 4px 12px; border-radius: 6px; border: 1px solid #64b5f6; background: rgba(100,181,246,0.12); color: #64b5f6; font-size: 12px; cursor: pointer; }
.btn-toggle:hover { background: rgba(100,181,246,0.22); }
.btn-backtest { padding: 4px 12px; border-radius: 6px; border: 1px solid #ff9800; background: rgba(255,152,0,0.12); color: #ff9800; font-size: 12px; cursor: pointer; }
.btn-backtest:hover { background: rgba(255,152,0,0.22); }
.btn-backtest:disabled { opacity: 0.5; cursor: wait; }
 .bt-result { font-size: 12px; font-weight: 600; align-self: center; }
 .bt-result.pos { color: #4caf50; }
 .bt-result.neg { color: #FF4D4F; }
 .bt-progress { display: flex; flex-direction: column; gap: 4px; align-items: flex-start; width: 220px; }
 .bt-progress-bar { width: 100%; height: 6px; border-radius: 3px; background: #2a2a3e; overflow: hidden; }
 .bt-progress-fill { height: 100%; border-radius: 3px; background: linear-gradient(90deg, #ff9800, #ffc107); transition: width 0.5s ease; }
 .bt-progress-label { font-size: 11px; color: #ff9800; }

/* 子页 tab */
.research-tabs { display: flex; gap: 6px; margin-bottom: 14px; border-bottom: 1px solid #2a2a3e; padding-bottom: 8px; }
.research-tabs .tab { padding: 6px 16px; border-radius: 6px 6px 0 0; border: 1px solid transparent; background: transparent; color: #999; font-size: 14px; cursor: pointer; }
.research-tabs .tab.active { background: #1a1a2e; border-color: #2a2a3e; border-bottom-color: #64b5f6; color: #64b5f6; font-weight: 600; }

/* 战法改名 */
.btn-rename { padding: 2px 8px; border-radius: 4px; border: 1px solid #2a2a3e; background: transparent; color: #64b5f6; font-size: 11px; cursor: pointer; margin-left: 6px; }
.btn-rename:hover { background: rgba(100,181,246,0.15); }
.name-input { padding: 2px 6px; border-radius: 4px; border: 1px solid #64b5f6; background: #0f0f23; color: #e0e0e0; font-size: 13px; width: 160px; }

/* 设置页 */
.settings-panel { background: #16162a; border: 1px solid #2a2a3e; border-radius: 8px; padding: 14px; }
.settings-title { font-size: 14px; font-weight: 600; color: #e0e0e0; margin-bottom: 14px; }
.setting-row { display: flex; align-items: center; gap: 14px; padding: 10px 0; border-bottom: 1px solid #22223a; }
.setting-info { flex: 1; }
.setting-label { font-size: 13px; font-weight: 600; color: #e0e0e0; }
.setting-desc { font-size: 12px; color: #888; margin-top: 4px; line-height: 1.5; }
.setting-state { font-size: 12px; color: #aaa; white-space: nowrap; }
.setting-hint { font-size: 11px; color: #777; margin-top: 10px; }
/* 开关样式 */
.switch { position: relative; display: inline-block; width: 44px; height: 22px; flex-shrink: 0; }
.switch input { opacity: 0; width: 0; height: 0; }
.switch .slider { position: absolute; cursor: pointer; inset: 0; background: #333; border-radius: 22px; transition: 0.3s; }
.switch .slider:before { content: ""; position: absolute; height: 16px; width: 16px; left: 3px; bottom: 3px; background: #888; border-radius: 50%; transition: 0.3s; }
.switch input:checked + .slider { background: #4caf50; }
.switch input:checked + .slider:before { transform: translateX(22px); background: #fff; }

.candidate-list { display: flex; flex-direction: column; gap: 10px; }
.candidate-card { background: #1a1a2e; border-radius: 8px; padding: 12px 14px; border: 1px solid #2a2a3e; }
.cand-header { display: flex; align-items: center; gap: 8px; margin-bottom: 10px; }
.cand-id { font-size: 14px; font-weight: 600; color: #e0e0e0; }
.cand-time { font-size: 12px; color: #888; margin-left: auto; }

.tag { font-size: 12px; padding: 1px 8px; border-radius: 4px; white-space: nowrap; }
.status-proposed { background: rgba(255,152,0,0.15); color: #ff9800; }
.status-approved { background: rgba(100,181,246,0.12); color: #64b5f6; }
.status-applied { background: rgba(76,175,80,0.15); color: #4caf50; }
.status-rejected { background: rgba(255,77,79,0.15); color: #FF4D4F; }
.tag-kind { margin-left: 4px; }
.kind-factor { background: rgba(100,181,246,0.12); color: #64b5f6; }
.kind-pattern { background: rgba(186,104,200,0.12); color: #ba68c8; }
.kind-weights { background: rgba(76,175,80,0.12); color: #4caf50; }
.kind-depth { background: rgba(255,152,0,0.12); color: #ff9800; }

/* 因子战法候选的可解释展示 */
.block-title { font-size: 12px; font-weight: 600; color: #aaa; margin: 10px 0 6px; }
.factors-row { display: flex; flex-wrap: wrap; gap: 8px; }
.factor-chip {
  display: inline-flex; align-items: center; gap: 6px;
  font-size: 12px; padding: 4px 10px; border-radius: 6px; background: #16162a; border: 1px solid #2a2a3e;
}
.cond-chip {
  display: inline-flex; align-items: center; gap: 6px;
  font-size: 12px; padding: 4px 10px; border-radius: 6px; background: #16162a; border: 1px solid #2a2a3e; color: #ba68c8;
}
.dir-badge { font-size: 11px; padding: 1px 6px; border-radius: 4px; font-weight: 600; }
.dir-badge.long { background: rgba(76,175,80,0.18); color: #4caf50; }
.dir-badge.short { background: rgba(255,77,79,0.18); color: #FF4D4F; }
.factor-name { color: #e0e0e0; font-weight: 600; }
.factor-id { color: #777; font-size: 11px; }
.factor-weight { color: #64b5f6; }
.factor-desc { font-size: 12px; color: #999; margin-top: 8px; line-height: 1.6; }

/* 大白话验证结论 */
.verify-plain { display: flex; flex-direction: column; gap: 8px; }
.plain-summary {
  display: flex; align-items: center; gap: 10px;
  padding: 8px 12px; border-radius: 8px; background: #14142a;
}
.plain-badge { font-size: 13px; font-weight: 700; padding: 3px 10px; border-radius: 6px; white-space: nowrap; }
.plain-badge.good { background: rgba(76,175,80,0.2); color: #4caf50; }
.plain-badge.bad { background: rgba(255,77,79,0.2); color: #FF4D4F; }
.plain-text { font-size: 12px; color: #ccc; line-height: 1.6; }
.plain-line { display: flex; gap: 8px; font-size: 12px; color: #bbb; line-height: 1.7; }
.plain-num { color: #64b5f6; font-weight: 600; flex-shrink: 0; }
.plain-body { flex: 1; }

/* 想细看的数字，折叠展示 */
.detail-block { margin-top: 10px; font-size: 12px; }
.detail-block summary { cursor: pointer; color: #64b5f6; font-size: 12px; }
.detail-block summary:hover { text-decoration: underline; }
.detail-row { display: flex; justify-content: space-between; gap: 12px; padding: 4px 2px; color: #999; }
.detail-row .d-label { color: #aaa; }
.detail-row .d-value { color: #bbb; font-variant-numeric: tabular-nums; }

.metric-row { display: flex; gap: 24px; flex-wrap: wrap; margin-bottom: 10px; }
.metric { display: flex; flex-direction: column; gap: 2px; }
.metric-label { font-size: 12px; color: #888; }
.metric-value { font-size: 16px; font-weight: 600; }
.metric-value.pos { color: #4caf50; }
.metric-value.neg { color: #FF4D4F; }

.weights-row { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 8px; }
.weight-chip { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; padding: 2px 8px; border-radius: 4px; background: #2a2a3e; color: #aaa; }
.weight-fid { color: #64b5f6; }
.weight-val { color: #e0e0e0; font-weight: 600; }

.reason-row { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; font-size: 13px; }
.reason-label { color: #888; }
.reason-text { color: #aaa; }

.depth-block { display: flex; flex-direction: column; gap: 8px; margin-bottom: 8px; }
.depth-stock { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; font-size: 12px; padding: 6px 8px; border-radius: 6px; background: #16162a; }
.depth-code { font-weight: 600; color: #e0e0e0; }
.depth-touch { color: #888; }
.order-chip { padding: 1px 8px; border-radius: 4px; white-space: nowrap; }
.order-chip.support { background: rgba(76,175,80,0.15); color: #4caf50; }
.order-chip.resistance { background: rgba(255,77,79,0.15); color: #FF4D4F; }

.cand-actions { display: flex; gap: 10px; }
.no-perm { font-size: 12px; color: #888; padding: 4px 0; }
.btn-approve { padding: 5px 14px; border-radius: 6px; border: 1px solid #4caf50; background: rgba(76,175,80,0.15); color: #4caf50; font-size: 13px; cursor: pointer; }
.btn-approve:hover { background: rgba(76,175,80,0.25); }
.btn-reject { padding: 5px 14px; border-radius: 6px; border: 1px solid #FF4D4F; background: rgba(255,77,79,0.1); color: #FF4D4F; font-size: 13px; cursor: pointer; }
.btn-reject:hover { background: rgba(255,77,79,0.2); }

@media (max-width: 768px) {
  .page-header { flex-wrap: wrap; gap: 8px; }
  .metric-row { gap: 16px; }
}

/* 回测任务中心（Tab 4：进度查看 + 任务添加） */
/* Backtest task center (Tab 4: progress view + task launch) */
.bt-center { display: flex; flex-direction: column; gap: 12px; }
.bt-add { background: #16162a; border: 1px solid #2a2a3e; border-radius: 8px; padding: 12px 14px; }
.bt-add-title { font-size: 13px; font-weight: 600; color: #e0e0e0; margin-bottom: 10px; }
.bt-add-row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.bt-select {
  padding: 6px 10px; border-radius: 6px; border: 1px solid #333;
  background: #0f0f23; color: #e0e0e0; font-size: 14px; outline: none; flex: 1; min-width: 220px;
}
.bt-select:disabled { opacity: 0.5; }
/* 阶段3.3 参数表单（时长 + 选股数 + 最小样本） */
.bt-params-row { gap: 10px; margin-bottom: 8px; flex-wrap: wrap; }
.bt-param { display: flex; align-items: center; gap: 4px; font-size: 12px; color: #999; }
.bt-input {
  width: 110px; padding: 5px 8px; border-radius: 6px; border: 1px solid #333;
  background: #0f0f23; color: #e0e0e0; font-size: 13px; outline: none;
}
.bt-input-sm { width: 64px; }
.bt-input:focus { border-color: #4a6cf7; }
/* 阶段3.2 运行控制按钮（暂停/继续/取消） */
.bt-ctl { padding: 3px 12px; font-size: 12px; }
.bt-ctl.bt-danger { border-color: rgba(255,77,79,0.5); color: #FF4D4F; }
.bt-ctl.bt-danger:hover { background: rgba(255,77,79,0.15); }
.bt-add-hint { font-size: 11px; color: #777; margin-top: 8px; line-height: 1.5; }
.bt-list { display: flex; flex-direction: column; gap: 10px; }
.bt-card { background: #1a1a2e; border-radius: 8px; padding: 10px 12px; border: 1px solid #2a2a3e; }
.bt-card.bt-error { border-color: rgba(255,77,79,0.4); }
.bt-card.bt-interrupted { border-color: rgba(255,152,0,0.4); }
.bt-head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 8px; }
.bt-cand { font-size: 12px; font-weight: 600; color: #e0e0e0; }
.bt-time { font-size: 11px; color: #777; margin-left: auto; }
.bt-status.status-proposed { background: rgba(100,181,246,0.15); color: #64b5f6; }
.bt-error { font-size: 12px; color: #FF4D4F; margin-bottom: 6px; }
.bt-actions { display: flex; gap: 8px; }
</style>