// Package trading §CANCEL-STATE（2026-10-28）：禁挂单 / 禁撤单的时段闸（纯函数、无 IO）。
//
// 背景（2026-10-28 600362 事故）：14:57:48 尾盘新挂买单 → 15:00:03 想撤 → 撞上收盘集合竞价
// （交易所只接受委托、不接受撤销）→ 本地却把"撤单请求被受理"当成"已撤"→ 实际成交既没有
// fills 也没有持仓，委托行永远挂在「已撤」。两个洞必须一起堵：
//
//	① 时段闸：交易所**不接受**撤单/新单的窗口，系统根本不该发起（不是发起后被拒）。
//	② 状态闸：请求受理 ≠ 交易所确认（见 store.orderStatusAdvance 与「撤单中」状态）。
//
// 时段口径（owner 2026-10-28 拍板，见 docs/PLAN_CANCEL_STATE_20261028.md §8）：
//
//	禁撤窗口 = 09:20–09:30（开盘集合竞价后段）、14:57–15:00（收盘集合竞价）
//	禁挂窗口 = 09:20–09:30（开盘集合竞价）、14:52–15:00（收盘清单 14:52 之后不再产生新单）
//
// 窗口按**北京时**判定（经 cntime.In），宿主机时区无关——这与全仓 §时区收口的纪律一致。
// English: pure time-window guards for "no new orders" / "no cancels", evaluated in Beijing time.
package trading

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"quant-trading-v2/internal/cntime"
)

// 出厂默认窗口（owner 2026-10-28 拍板）。格式 "HHMM-HHMM"，闭区间左开右闭右端？
// 语义为 **[起点, 终点)**：09:20–09:30 覆盖 09:20:00~09:29:59，09:30:00 起恢复。
// 为什么是左闭右开：09:30:00 集合竞价撮合结束、连续竞价开始，交易所此时开始接受撤单。
const (
	// DefaultNoCancelWindows 禁撤窗口：开盘集合竞价后段 + 收盘集合竞价。
	DefaultNoCancelWindows = "09:20-09:30,14:57-15:00"
	// DefaultNoNewOrderWindows 禁挂窗口：开盘集合竞价 + 收盘清单之后。
	DefaultNoNewOrderWindows = "09:20-09:30,14:52-15:00"
)

// windowSpec 一个时段窗（北京时间，分钟为粒度）。
type windowSpec struct {
	startMin int // 当日 00:00 起的分钟数
	endMin   int // 当日 00:00 起的分钟数（不含）
	label    string
}

// parseWindows 解析 "HH:MM-HH:MM,HH:MM-HH:MM" 形式的窗口串。
// 解析失败不静默：坏配置会让闸门形同虚设（比"闸门太严"危险得多），故此处直接返回错误，
// 由调用方落成显式告警 + 按出厂默认兜底。
// English: parse a window list; malformed entries are errors (a silently-dropped window
// would silently disable the guard it exists to provide).
func parseWindows(spec string) ([]windowSpec, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	var out []windowSpec
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		segs := strings.Split(part, "-")
		if len(segs) != 2 {
			return nil, fmt.Errorf("窗口格式非法 %q（应为 HH:MM-HH:MM）", part)
		}
		start, err := parseHHMM(segs[0])
		if err != nil {
			return nil, fmt.Errorf("窗口起点非法 %q: %w", segs[0], err)
		}
		end, err := parseHHMM(segs[1])
		if err != nil {
			return nil, fmt.Errorf("窗口终点非法 %q: %w", segs[1], err)
		}
		if start == end {
			// 起止相同＝空窗口，直接忽略而不是当成"整天禁止"（后者会锁死整条链路）。
			continue
		}
		out = append(out, windowSpec{startMin: start, endMin: end, label: part})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].startMin < out[j].startMin })
	return out, nil
}

// parseHHMM 解析 "HH:MM" 或 "HHMM" 为当日分钟数。
func parseHHMM(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("空值")
	}
	if !strings.Contains(s, ":") {
		if len(s) != 4 {
			return 0, fmt.Errorf("应为 HHMM")
		}
		h, m := atoiSafe(s[0:2]), atoiSafe(s[2:4])
		if h < 0 || m < 0 || h > 23 || m > 59 {
			return 0, fmt.Errorf("越界")
		}
		return h*60 + m, nil
	}
	segs := strings.Split(s, ":")
	if len(segs) != 2 {
		return 0, fmt.Errorf("应为 HH:MM")
	}
	h, m := atoiSafe(segs[0]), atoiSafe(segs[1])
	if h < 0 || m < 0 || h > 23 || m > 59 {
		return 0, fmt.Errorf("越界")
	}
	return h*60 + m, nil
}

// atoiSafe 字符串转非负整数（失败返回 -1）。
func atoiSafe(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return -1
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// inWindow 判断北京时落在任一窗口内。
func inWindow(specs []windowSpec, now time.Time) (bool, string) {
	if len(specs) == 0 {
		return false, ""
	}
	bj := cntime.In(now)
	min := bj.Hour()*60 + bj.Minute()
	for _, w := range specs {
		if min >= w.startMin && min < w.endMin {
			return true, w.label
		}
	}
	return false, ""
}

// SessionWindows §CANCEL-STATE：禁挂/禁撤时段的判定器（由 Controller 持有）。
//
// 结构体只装解析后的窗口表；判定是纯函数式的一查表，不碰时钟（时钟由调用方注入 now），
// 所以测试可以固定时刻而不依赖真实时间。
// English: the guard object; the clock is injected by the caller so tests are deterministic.
type SessionWindows struct {
	noCancel   []windowSpec
	noNewOrder []windowSpec
	// raw 保留原始配置串，用于回显"当前禁挂/禁撤窗口"（运维面要能看到规则本身）。
	rawCancel   string
	rawNewOrder string
}

// NewSessionWindows 构造窗口闸。配置串为空/解析失败时回退出厂默认并把解析错误一并返回，
// 调用方必须把错误落成告警——配置写错却静默生效是本仓反复出现过的形态。
// English: builds the guard; a malformed spec falls back to factory defaults and returns the error.
func NewSessionWindows(noCancelSpec, noNewOrderSpec string) (*SessionWindows, error) {
	var errs []string
	cancelSpecs, err := parseWindows(noCancelSpec)
	if err != nil {
		errs = append(errs, "禁撤窗口: "+err.Error())
	}
	newSpecs, err := parseWindows(noNewOrderSpec)
	if err != nil {
		errs = append(errs, "禁挂窗口: "+err.Error())
	}
	if cancelSpecs == nil {
		cancelSpecs, _ = parseWindows(DefaultNoCancelWindows)
	}
	if newSpecs == nil {
		newSpecs, _ = parseWindows(DefaultNoNewOrderWindows)
	}
	sw := &SessionWindows{
		noCancel: cancelSpecs, noNewOrder: newSpecs,
		rawCancel: noCancelSpec, rawNewOrder: noNewOrderSpec,
	}
	if len(errs) > 0 {
		return sw, fmt.Errorf("时段窗口配置异常(已回退出厂默认): %s", strings.Join(errs, "; "))
	}
	return sw, nil
}

// DefaultSessionWindows 出厂窗口闸（配置未提供时用）。
func DefaultSessionWindows() *SessionWindows {
	sw, _ := NewSessionWindows(DefaultNoCancelWindows, DefaultNoNewOrderWindows)
	return sw
}

// CancelBlocked 判断 now 是否落在禁撤窗口内。第二个返回值是命中的窗口标签（空=未命中）。
// English: whether a cancel request must be refused right now.
func (sw *SessionWindows) CancelBlocked(now time.Time) (bool, string) {
	if sw == nil {
		return false, ""
	}
	return inWindow(sw.noCancel, now)
}

// NewOrderBlocked 判断 now 是否落在禁挂窗口内。第二个返回值是命中的窗口标签（空=未命中）。
// English: whether a new order must be refused right now.
func (sw *SessionWindows) NewOrderBlocked(now time.Time) (bool, string) {
	if sw == nil {
		return false, ""
	}
	return inWindow(sw.noNewOrder, now)
}

// Describe 供运维面回显**当前生效**的窗口规则。
//
// §FIX-20261028-F05（2026-10-29）：raw 是配置原文，配置为空串时生效的是出厂默认
//（见 NewSessionWindows 的回落分支），而旧实现把空串原样回出去 ⇒ 界面会显示
//「禁撤 （空）」这种无意义读数。现在空串回落该侧的默认串——回显的必须是**判定真正用的值**。
// English: echoes the EFFECTIVE windows (empty raw config falls back to factory defaults),
// because the UI must show what the guard actually enforces.
func (sw *SessionWindows) Describe() map[string]string {
	if sw == nil {
		return map[string]string{"no_cancel_windows": DefaultNoCancelWindows, "no_new_order_windows": DefaultNoNewOrderWindows}
	}
	cancel, newOrder := sw.rawCancel, sw.rawNewOrder
	if strings.TrimSpace(cancel) == "" {
		cancel = DefaultNoCancelWindows
	}
	if strings.TrimSpace(newOrder) == "" {
		newOrder = DefaultNoNewOrderWindows
	}
	return map[string]string{"no_cancel_windows": cancel, "no_new_order_windows": newOrder}
}

// ErrNoCancelWindow 禁撤窗口的拒绝文案（前端要能直接展示给用户看）。
func ErrNoCancelWindow(label string) error {
	return fmt.Errorf("当前处于不可撤单时段（%s）：集合竞价期间交易所不接受撤单，请稍后重试", label)
}

// ErrNoNewOrderWindow 禁挂窗口的拒绝文案。
func ErrNoNewOrderWindow(label string) error {
	return fmt.Errorf("当前处于不可挂单时段（%s）：集合竞价/尾盘窗口不再接受新委托，请稍后重试", label)
}

// isCancellableStatus 判断委托状态是否仍属"可（再）撤"的在途形态。
//
// §CANCEL-STATE（2026-10-28）：「撤单中」也纳入——撤单请求已发出但交易所尚未确认，
// 它仍可能在途；紧急停止必须能再撤一次（幂等：网关对已撤单会明确失败，进失败明细）。
// 旧别名「已报待撤」「部成待撤」继续识别（新写入只用「撤单中」）。
// English: in-flight statuses a cancel request may still target (incl. 撤单中).
func isCancellableStatus(status string) bool {
	switch status {
	case "已报", "部成", "撤单中", "已报待撤", "部成待撤":
		return true
	}
	return false
}
