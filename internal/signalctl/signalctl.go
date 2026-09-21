// Package signalctl 信号控制器：战法与交易执行之间的唯一准入/持续性监测决策组件（§SIGNAL_CONTROLLER 20260917）。
//
// 架构定位（docs/SIGNAL_CONTROLLER_PLAN_20260917.md）：
//   - 上游一个入口：流程引擎每轮把战法产出的【全量活跃信号集】喂给 Evaluate；
//   - 下游两条出口：返回每个信号在 live（实盘）/ paper（模拟盘）两通道的显式裁定（pass/hold/block+原因），
//     流程引擎按裁定分别触发交易器与模拟盘——两者仍是独立执行器，本包不含任何下单/撮合逻辑；
//   - 纯逻辑组件：无 IO、无账本引用，配置快照与时间由调用方注入，100% 可单测。
//
// 收编的历史闸门（迁移映射见方案 §四）：
//   - 战法白名单（原 autoPlace 内联比对 + risk.Gate.checkWhitelist 双写 → 本包 AdmitStrategy 单一实现）；
//   - 个股黑名单 + 板块黑名单（原仅实盘下单侧消费 → 两通道信号端统一，黑名单拦截默认影子模式）；
//   - 买入持续性监测（原 engine.realBuyConfirmPass 与 paper.buyConfirm 两份状态机 → 本包唯一实现，
//     键空间 (channel, account, strategyKey, code)，双通道参数独立）。
//
// English: signalctl is the single admission + persistence-monitoring component between strategy
// signal generation and execution. One upstream entry (the flow engine feeds the FULL active signal
// set each round), two downstream exits (explicit per-channel pass/hold/block verdicts for the live
// trader and the paper book, which stay pure executors). Pure logic: no IO, no ledger reference.
package signalctl

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
)

// Channel 消费通道：实盘 / 模拟盘。一个信号在两通道各有独立裁定与探针状态。
// （Channel: the consumer side — live or paper; each has its own verdict & probe state.）
type Channel string

const (
	// ChannelLive 实盘通道：裁定结果驱动真实下单执行器。
	ChannelLive Channel = "live"
	// ChannelPaper 模拟盘通道：裁定结果仅驱动纸面撮合，不影响实盘。
	ChannelPaper Channel = "paper"
)

// Verdict 裁定结果三态：pass=放行交易、hold=持续性监测观察中（探针未满确认窗）、block=准入拒绝。
// （Verdict: pass (trade), hold (persistence window not met) or block (admission denied).）
type Verdict string

const (
	// VerdictPass 放行：交由该通道执行器继续处理。
	VerdictPass Verdict = "pass"
	// VerdictHold 暂缓：持续性探针在确认窗观测中，本轮不执行。
	VerdictHold Verdict = "hold"
	// VerdictBlock 拒绝：准入校验未通过，不进入任何执行流程。
	VerdictBlock Verdict = "block"
)

// Stage 裁定发生层（可观测性：一眼区分"被什么拦了"）。
// （Stage: which layer produced the decision.）
const (
	StageStrategy      = "strategy"         // 战法白名单
	StageCodeBlacklist = "code_blacklist"   // 个股黑名单
	StageSectorBlack   = "sector_blacklist" // 板块黑名单
	StageConfirm       = "confirm"          // 持续性确认窗
	StagePassthrough   = "passthrough"      // 非买入/开仓信号直通（提醒、卖出、watch）
)

// Decision 单信号单通道裁定。留痕结构体（Recent 环形缓冲与 HTTP 端点共用形状）。
// （A per-channel decision for one signal; also the audit-record shape.）
type Decision struct {
	Channel  Channel   `json:"channel"`
	Account  string    `json:"account,omitempty"` // §D-1 留痕归属账号（多账号共享/独立引擎统一带出）
	Verdict  Verdict   `json:"verdict"`
	Stage    string    `json:"stage"`
	Code     string    `json:"code"`
	Name     string    `json:"name,omitempty"`
	Strategy string    `json:"strategy"`         // 显示名（与消息中心一致）
	SKey     string    `json:"strategy_key"`     // 规范战法键（StrategyKeyOf）
	Reason   string    `json:"reason,omitempty"` // block/hold 必填
	Shadow   bool      `json:"shadow,omitempty"` // true=影子命中（会拦但未拦，新行为观察期）
	At       time.Time `json:"at"`
}

// Policy 单通道准入策略快照（由流程引擎每轮装配下发；控制器不读配置文件、不持 Manager）。
// （Per-channel policy snapshot assembled and pushed by the flow engine each round.）
type Policy struct {
	// Strategies 战法白名单（规范键：dragon/double_bump/n_shape/dragon_return/momentum/fac_1/pat_1；
	// 兼容存量显示名/规则ID条目）。空=默认全集：内置四形态+库规则前缀放行，
	// 动量/未知战法必须显式列名（§20260917 动量误交易根修语义）。
	Strategies []string
	// CodeBlacklist 个股黑名单（纯代码比对，与 risk 执行层 config.CodeInBlacklist 同口径）。
	CodeBlacklist []string
	// SectorBlacklist 板块黑名单（信号 Sector 精确匹配；空=不启用）。
	SectorBlacklist []string
	// ShadowBlacklist 黑名单影子模式：true=命中只留痕不拦截（黑名单补齐到模拟盘属新行为，
	// 方案 §八 灰度要求观察期）；战法白名单不受本标志影响、始终硬拦。
	ShadowBlacklist bool
	// Discipline 持续性参数（探针+确认窗；低置信 BuyConfirmMin 分钟 / 高置信 BuyConfirmHighSec 秒；
	// 两窗均 ≤0 = 关闭持续性监测，买入直通——与旧 paper discipline 缺省语义一致）。
	Discipline config.DisciplineConfig
}

// probeKey 持续性探针状态键：(通道, 账号, 战法键, 代码)。
// 含账号维度——多账号共享引擎时实盘/模拟盘的确认态天然按账号隔离（方案 §四.1）。
// （Probe state key incl. account so shared engines keep per-account confirmation state.）
type probeKey struct {
	ch       Channel
	account  string
	strategy string
	code     string
}

// Controller 信号控制器。可并发（5s 近实时循环与 5min 主循环可同时调用）。
// （The controller itself; concurrency-safe across the 5s and main-loop feeds.）
type Controller struct {
	mu     sync.Mutex
	probes map[probeKey]time.Time // 买入信号首次出现时刻（连续存在累计，消失即重置）
	// §SELLPOINT-UNIFY 卖出裁决跨轮状态（键=通道+账号+代码；与买入探针同构，live/paper 隔离）。
	// 由 JudgeSell 惰性建表，平仓/换仓由 PruneSellStates/ResetSell 清理（internal/signalctl/sell.go）。
	sellStates map[sellKey]*sellState
	ring       []Decision // 裁定留痕环形缓冲（最新在尾）
	cap        int
	// §D-2（GAP_VERIFY_20260917_PM）留痕落盘目录：非空时 record() 逐条追写
	// verdicts-YYYYMMDD.jsonl（按日自然轮转），重启经 AttachAudit 回灌当日环——
	// 此前纯内存 512 环，quant 任何一次重启即丢"为何没成交"的拦截原因。
	auditDir string
}

// New 创建控制器（留痕环默认 512 条）。
func New() *Controller {
	return &Controller{probes: make(map[probeKey]time.Time), sellStates: make(map[sellKey]*sellState), cap: 512}
}

// auditFileFor 当日留痕 JSONL 路径（按 d.At 的本地日期分文件，跨日自动切换）。
func auditFileFor(dir string, at time.Time) string {
	return filepath.Join(dir, "verdicts-"+at.Format("20060102")+".jsonl")
}

// AttachAudit 绑定留痕落盘目录并回灌当日历史裁定（进程启动装配一次；
// 目录为空串=纯内存模式，与旧行为一致）。回灌按 (at,channel,account,code,skey,verdict)
// 与现有环去重，超容量保留最新。
// English: §D-2 — binds the JSONL audit dir and replays today's verdicts into the ring so a
// restart no longer wipes "why was this signal blocked" evidence.
func (c *Controller) AttachAudit(dir string) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.Open(auditFileFor(dir, time.Now()))
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	} else {
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 256*1024)
		c.mu.Lock()
		seen := make(map[[6]string]bool, len(c.ring))
		keyOf := func(d Decision) [6]string {
			return [6]string{d.At.String(), string(d.Channel), d.Account, d.Code, d.SKey, string(d.Verdict)}
		}
		for _, d := range c.ring {
			seen[keyOf(d)] = true
		}
		for sc.Scan() {
			var d Decision
			if json.Unmarshal(sc.Bytes(), &d) != nil {
				continue // 脏行跳过（进程被杀时的半行不影响后续）
			}
			if seen[keyOf(d)] {
				continue
			}
			c.ring = append(c.ring, d)
		}
		if len(c.ring) > c.cap {
			c.ring = append([]Decision(nil), c.ring[len(c.ring)-c.cap:]...)
		}
		c.mu.Unlock()
	}
	c.mu.Lock()
	c.auditDir = dir
	c.mu.Unlock()
	return nil
}

// StrategyKeyOf 解析信号的规范战法键（白名单/探针统一键空间）：
// StrategyType 优先（runner 规范 ID 或库规则 ID），回退 StrategyID、再回退显示名归一。
// English: canonical strategy key — StrategyType first (runner id / library rule id), then
// StrategyID, then display-name normalization.
func StrategyKeyOf(sig combat_agent.Signal) string {
	// 优先取规范战法 ID（runner 规范 ID 或库规则 ID），与白名单/前端开关同键空间。
	if sig.StrategyType != "" {
		return sig.StrategyType
	}
	// 无规范 ID 时回退战法库规则 ID。
	if sig.StrategyID != "" {
		return sig.StrategyID
	}
	// 都没有时按显示名归一映射到内置键；无法映射则原样返回显示名。
	switch combat_agent.NormalizeStrategyName(sig.Strategy) {
	case "龙头":
		return "dragon"
	case "双响炮":
		return "double_bump"
	case "N形":
		return "n_shape"
	case "龙回头":
		return "dragon_return"
	case "动量":
		return "momentum"
	// §WMQ-2（20260917）：做空四战法归一映射。旧实现对显示名原样返回（passthrough），
	// 与白名单/前端开关的键空间脱节——做空信号若未来接入准入闸将 100% 被视为未知键。
	// 规范键与 internal/strategies/{high_churn,break_down,leader_decay,good_news_fade} 的
	// runner 规范 ID 一致。
	case "高位滞涨":
		return "high_churn"
	case "放量破位":
		return "break_down"
	case "龙头断板":
		return "leader_decay"
	case "利好兑现砸盘":
		return "good_news_fade"
	}
	return sig.Strategy
}

// AdmitStrategy 战法白名单成员判定（纯函数，风控闸 risk.Gate 与控制器共用，杜绝双写漂移）：
//   - 白名单非空 → 必须显式命中（键=规范键 ∪ 显示名 ∪ StrategyID 三键任一相等）；
//   - 白名单为空（前端"全部开启"存量语义）→ 放行内置四形态与库规则（fac_/pat_ 前缀及聚合池
//     factor/pattern），动量与其他未知键拒绝。
//
// English: whitelist membership shared by the controller and the risk gate. A non-empty list must
// contain the key (canonical id / display name / rule id all match). The legacy EMPTY list means
// "all known built-ins on": the four form strategies plus library rules — momentum and unknown
// keys are rejected.
func AdmitStrategy(pol Policy, sig combat_agent.Signal) (bool, string) {
	// 解析规范键；无法识别来源战法直接拒绝。
	key := StrategyKeyOf(sig)
	if key == "" {
		return false, "无法识别信号来源战法"
	}
	// 分支一：白名单为空（前端"全部开启"存量语义）→ 只放行内置四形态与库规则聚合池；
	// 动量与一切未知键必须显式列名，防止动量误交易（§20260917 根修语义）。
	if len(pol.Strategies) == 0 {
		switch key {
		case "dragon", "double_bump", "n_shape", "dragon_return", "factor", "pattern":
			return true, ""
		}
		// 库规则前缀（fac_1/pat_2…）视同已知放行。
		if strings.HasPrefix(key, "fac_") || strings.HasPrefix(key, "pat_") {
			return true, ""
		}
		return false, fmt.Sprintf("战法 %q 未在开关列表（默认全集不含动量/未知来源）", key)
	}
	// 分支二：白名单非空 → 必须"规范键 ∪ 显示名 ∪ 规则 ID"三键任一显式命中。
	for _, s := range pol.Strategies {
		if s == key || s == sig.Strategy || s == sig.StrategyID {
			return true, ""
		}
	}
	return false, fmt.Sprintf("战法 %q 白名单外", key)
}

// Admit 单信号裁定（推进确认窗状态机——探针推进本就是裁定语义的一部分）。
// §F-8（20260917 缺陷修复批）：旧 admit 尾参 advance 从未被函数体读取（"说谎参数"，
// 注释宣称的只读判定模式从未实现），两调用方恒传 true——参数删除，语义不变。
func (c *Controller) Admit(ch Channel, account string, sig combat_agent.Signal, pol Policy, now time.Time) Decision {
	return c.admit(ch, account, sig, pol, now)
}

// Evaluate 批量裁定（流程引擎每轮唯一入口）：
//  1. 清理本轮活跃集中不存在的买入探针（信号连续性要求，消失即重置）；
//  2. 逐信号产出本通道裁定；买入信号推进确认窗状态机。
//     返回与入参等长的裁定切片（下标对齐）。
//
// English: batch evaluation — prune stale probes against this round's active buy set, then decide
// every signal, advancing the confirm state machine for buys. The returned slice is index-aligned.
func (c *Controller) Evaluate(ch Channel, account string, sigs []combat_agent.Signal, pol Policy, now time.Time) []Decision {
	c.mu.Lock()
	// 记录本轮活跃的买入信号探针键（先建 seen 集合用于清理僵尸探针）。
	seen := make(map[probeKey]bool, len(sigs))
	for _, s := range sigs {
		if isTradeBuy(s) {
			seen[probeKey{ch, account, StrategyKeyOf(s), s.Code}] = true
		}
	}
	// 清理本通道/本账号下，本轮活跃集中不存在的探针（信号消失即重置，
	// 连续性要求：中断后重新出现需重新计时），以及超过最长存活窗的探针（超龄僵尸记录）。
	for k, first := range c.probes {
		if k.ch != ch || k.account != account {
			continue
		}
		if !seen[k] || now.Sub(first) > maxConfirmWindow(pol.Discipline) {
			delete(c.probes, k) // 信号消失或探针超龄（僵尸记录）→ 重置
		}
	}
	c.mu.Unlock()

	// 第二阶段：逐信号裁定（与入参下标对齐返回）；买入信号的确认窗流推进在 admit 内完成。
	out := make([]Decision, 0, len(sigs))
	for _, s := range sigs {
		d := c.admit(ch, account, s, pol, now)
		out = append(out, d)
	}
	return out
}

// admit 内部裁定：买入信号推进确认窗状态机并留痕，非买入直通。
func (c *Controller) admit(ch Channel, account string, sig combat_agent.Signal, pol Policy, now time.Time) Decision {
	d := Decision{Channel: ch, Account: account, Verdict: VerdictPass, Code: sig.Code, Name: sig.Name,
		Strategy: sig.Strategy, SKey: StrategyKeyOf(sig), At: now}

	// 非"买入/开仓"方向全部直通：卖出、离场、止盈止损提醒、watch 观察不参与准入控制
	// （拦退出=强迫扛单；watch 本就不进撮合）。做空开仓受全局 short_enabled 与融券池独立门控，
	// 不在战法白名单语义内，直通。
	if !isTradeBuy(sig) {
		d.Verdict = VerdictPass
		d.Stage = StagePassthrough
		return d
	}

	// 1. 战法白名单（硬拦，不受影子标志影响——准入漂移是事故类，不是可灰度的新行为）
	if ok, reason := AdmitStrategy(pol, sig); !ok {
		d.Verdict, d.Stage, d.Reason = VerdictBlock, StageStrategy, reason
		c.record(d)
		return d
	}
	// 2. 个股黑名单（影子标志适用）：命中后交 blockOrShadow 决定硬拦或影子留痕。
	if config.CodeInBlacklist(pol.CodeBlacklist, sig.Code) {
		d.Stage = StageCodeBlacklist
		d.Reason = fmt.Sprintf("个股黑名单(%s)", sig.Code)
		return c.blockOrShadow(&d, pol)
	}
	// 3. 板块黑名单（影子标志适用）：Sector 为空的信号无法判定板块，跳过。
	if sig.Sector != "" && inStringList(pol.SectorBlacklist, sig.Sector) {
		d.Stage = StageSectorBlack
		d.Reason = fmt.Sprintf("板块黑名单(%s)", sig.Sector)
		return c.blockOrShadow(&d, pol)
	}
	// 4. 持续性监测（探针+确认窗；双窗≤0=未启用，买入直通）
	disc := pol.Discipline
	// 低置信窗：分钟级；高置信快车道窗：秒级。
	lowWin := time.Duration(disc.BuyConfirmMin) * time.Minute
	highWin := time.Duration(disc.BuyConfirmHighSec) * time.Second
	// 两窗均未配置 → 关闭持续性监测，买入直接放行（与旧 paper discipline 缺省语义一致）。
	if lowWin <= 0 && highWin <= 0 {
		return d
	}
	// 探针键含通道+账号+战法键+代码，多账号/双通道确认态互不干扰。
	key := probeKey{ch, account, d.SKey, sig.Code}
	c.mu.Lock()
	first, tracked := c.probes[key]
	// 首次观测：登记首次出现时刻，本轮视为 hold（只观测不执行）。
	if !tracked {
		c.probes[key] = now
	}
	c.mu.Unlock()
	if !tracked {
		d.Verdict, d.Stage = VerdictHold, StageConfirm
		d.Reason = "买入信号待确认(探针观测中)"
		c.record(d)
		return d
	}
	// 默认走低置信（分钟级）窗；高置信信号换秒级快窗。
	win := lowWin
	// 置信度口径归一：Signal.Confidence 为 0~1（显示×100），阈值存百分数（默认 85），
	// 先放大再比，避免 0.9 ≥ 85 恒假导致高置信快车道永失（与旧实盘闸同修）。
	if sig.Confidence*100 >= highConfThreshold(disc) {
		win = highWin
	}
	// 确认窗未满：仍处于观测期，hold 并提示剩余时长。
	if now.Sub(first) < win {
		d.Verdict, d.Stage = VerdictHold, StageConfirm
		d.Reason = fmt.Sprintf("确认窗未满(%.0fs/%.0fs)", now.Sub(first).Seconds(), win.Seconds())
		c.record(d)
		return d
	}
	// 探针已满窗确认：清探针放行（重复下单由执行层幂等键兜底）。
	c.mu.Lock()
	delete(c.probes, key) // 确认通过，清除探针（下单/撮合由执行层幂等键兜底重复）
	c.mu.Unlock()
	return d
}

// blockOrShadow 影子模式：命中留痕但裁定 pass（新行为观察期用，方案 §八）。
func (c *Controller) blockOrShadow(d *Decision, pol Policy) Decision {
	// 影子模式（ShadowBlacklist=true）：裁定仍为 pass，但标注 Shadow 留痕——
	// 黑名单补齐属新行为，观察期内"会拦但未拦"，先积累影子命中数据再灰度转硬拦。
	if pol.ShadowBlacklist {
		d.Shadow = true
		c.record(*d)
		return *d
	}
	// 正常模式：硬拦（block）。
	d.Verdict = VerdictBlock
	c.record(*d)
	return *d
}

// record 写入留痕环（最新在尾，满则丢最旧），并按日追写 JSONL 审计文件（若已绑定）。
// 落盘失败静默降级为纯内存（留痕是旁路观测面，绝不阻断裁定主流程）。
// English: appends to the ring and, when §D-2 audit is attached, to the per-day JSONL file.
// Write failures degrade silently to memory-only — the audit trail must never block admission.
func (c *Controller) record(d Decision) {
	c.mu.Lock()
	// 追加到尾部。
	c.ring = append(c.ring, d)
	// 超容量时重建切片，只保留最新 cap 条（丢弃最旧）。
	if len(c.ring) > c.cap {
		c.ring = append([]Decision(nil), c.ring[len(c.ring)-c.cap:]...)
	}
	// §D-2 落盘（持锁写，串行化多 goroutine 追写，避免 JSONL 行交错撕裂）。
	if c.auditDir != "" {
		if b, err := json.Marshal(d); err == nil {
			if f, ferr := os.OpenFile(auditFileFor(c.auditDir, d.At), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); ferr == nil {
				f.Write(b)
				f.Write([]byte("\n"))
				f.Close()
			}
		}
	}
	c.mu.Unlock()
}

// Recent 返回最近 limit 条裁定留痕（最新在前），供审计端点消费。
// （Recent returns the newest-first audit tail.）
func (c *Controller) Recent(limit int) []Decision {
	c.mu.Lock()
	defer c.mu.Unlock()
	if limit <= 0 || limit > len(c.ring) {
		limit = len(c.ring)
	}
	out := make([]Decision, 0, limit)
	for i := len(c.ring) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, c.ring[i])
	}
	return out
}

// isTradeBuy 是否"买入/开仓"方向信号（仅此类参与准入+持续性裁定）。
// 做多 buy 与 买入 兼容两代 Action 写法。
func isTradeBuy(s combat_agent.Signal) bool {
	if s.Direction == "做空" {
		return false // 卖出平多/做空开仓均不走多头白名单（做空受全局开关独立门控）
	}
	return s.Action == "buy" || s.Action == "买入"
}

// inStringList 精确匹配列表成员（板块名中文精确比对，无后缀语义）。
func inStringList(list []string, v string) bool {
	for _, x := range list {
		if strings.TrimSpace(x) == v {
			return true
		}
	}
	return false
}

// maxConfirmWindow 探针最长存活窗（超龄僵尸清理）：取两窗较大 + 5min 余量。
func maxConfirmWindow(d config.DisciplineConfig) time.Duration {
	low := time.Duration(d.BuyConfirmMin) * time.Minute
	high := time.Duration(d.BuyConfirmHighSec) * time.Second
	if high > low {
		low = high
	}
	return low + 5*time.Minute
}

// highConfThreshold 高置信阈值归一（默认 85 百分数）。
func highConfThreshold(d config.DisciplineConfig) float64 {
	if d.HighConfThreshold <= 0 {
		return 85
	}
	return d.HighConfThreshold
}
