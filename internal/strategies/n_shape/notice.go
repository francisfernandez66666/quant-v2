// notice.go — §B6（2026-11-05 通知增强）n形买入通知的要素与文案构造。
//
// 施工图 §5A.1 把「n形买入」定成**必推 + 多通道并发 + 不折叠**，文案要素缺一不可：
// 票 + 形态阶段 + D1/情绪/是否龙头三个输入值 + 有效期。
// 分级与出站（预算/冷却/通道）归 internal/newsagent，本文件只负责"这条消息长什么样、
// 算不算买入档"——依赖方向保持 strategies → newsagent（newsagent 不 import 战法包）。
package n_shape

import (
	"fmt"
	"strings"
)

// 买入通知的有效期口径：N 形的时间优先级只在 9:15~15:00 的盘中窗口内成立（priorityOf 之外一律 mute），
// 所以"有效期"按当日窗口写死到 15:00，不引入新的可配置 TTL——多一个旋钮就多一处漂移。
// English: the notice's validity is the intraday priority window (9:15~15:00); no new TTL knob.
const buyNoticeExpiry = "当日 15:00（时间优先级窗口收盘即失效）"

// BuyNotice 一条 n形买入通知的要素。实现了 newsagent.BuyNotice 接口
// （BuyCode/BuyName/BuyText/BuyTier 四个方法），由分级器决定去向、通道与冷却。
// English: BuyNotice carries one N-shape buy's fields and satisfies newsagent.BuyNotice.
type BuyNotice struct {
	Code     string  // 股票代码（Bare stock code）
	Name     string  // 股票名称（Stock name）
	Stage    string  // 形态阶段（"左侧一突"/"右侧二突"/"完整链"）（Shape stage）
	D1       float64 // D1 事件分（0~40）（D1 event score）
	Emotion  string  // 情绪周期相位（"启动"/"高潮"/"退潮"…）（Emotion phase）
	IsLeader bool    // 是否板块龙头（Whether it is the sector leader）
	Priority int     // 时间优先级基础分（0~100）（Time-priority base score）
	Remind   string  // 时间优先级标签（strong/observe/mute，来自 priorityOf）（Priority label from priorityOf）
	CanOpen  bool    // 是否允许开仓（同 priorityOf.CanOpen）（Whether opening is allowed）
	Expiry   string  // 有效期说明（空串=按当日窗口默认）（Validity note; empty = intraday default）
}

// BuyCode 代码（接口方法）。English: BuyCode returns the stock code.
func (b BuyNotice) BuyCode() string { return b.Code }

// BuyName 名称（接口方法）。English: BuyName returns the stock name.
func (b BuyNotice) BuyName() string { return b.Name }

// BuyTier 是否落在买入档：时间优先级出 strong 且允许开仓才算（与 priorityOf 的三档标签同源）。
// 观察档/静默档不许占用必推通道——那是"看看就好"，不是"要动手"。
// English: BuyTier reports the strong+can-open band only; observe/mute never take the must-push lane.
func (b BuyNotice) BuyTier() bool { return b.Remind == "strong" && b.CanOpen }

// IsBuyTier 给评分结果直接判买入档（priorityOf 的强分+可开仓两条一起看，缺一条都不算）。
// English: IsBuyTier judges a PriorityResult directly: strong label AND can-open, both required.
func (p PriorityResult) IsBuyTier() bool { return p.Label == "strong" && p.CanOpen }

// BuyText 出站文案：票 + 形态阶段 + D1/情绪/是否龙头 + 有效期（§5A.1 要求缺一不可）。
// English: BuyText renders the message: stock, stage, the three inputs and the validity window.
func (b BuyNotice) BuyText() string {
	expiry := strings.TrimSpace(b.Expiry)
	if expiry == "" {
		expiry = buyNoticeExpiry
	}
	leader := "非龙头"
	if b.IsLeader {
		leader = "板块龙头"
	}
	emotion := strings.TrimSpace(b.Emotion)
	if emotion == "" {
		emotion = "未给情绪相位"
	}
	stage := strings.TrimSpace(b.Stage)
	if stage == "" {
		stage = "完整链"
	}
	return fmt.Sprintf("标的：%s(%s)｜形态阶段：%s｜D1=%.1f｜情绪=%s｜%s｜时间优先级=%d｜有效期：%s",
		b.Name, b.Code, stage, b.D1, emotion, leader, b.Priority, expiry)
}

// BuyStage 形态阶段串：右侧二突 > 左侧一突 > 完整链（越靠后确认越强，文案要点名到阶段）。
// English: BuyStage names the shape stage (right breakout > left breakout > full chain).
func (r *ScoreResult) BuyStage() string {
	switch {
	case r.RightSignal:
		return "右侧二突"
	case r.LeftSignal:
		return "左侧一突"
	default:
		return "完整链"
	}
}

// BuyNoticeFrom 由一次评分结果构造买入通知（战法侧唯一的装配点，避免各调用点手搓字段漏一项）。
// res 为 nil 或不在买入档时返回 ok=false，调用方据此什么都不发。
// English: BuyNoticeFrom assembles the notice from one ScoreResult; ok=false when not in the buy band.
func BuyNoticeFrom(code, name string, res *ScoreResult, emotion string, isLeader bool) (BuyNotice, bool) {
	if res == nil {
		return BuyNotice{}, false
	}
	// 买入资格单一来源：Valid（D1>0 且总分≥60）+ CanOpen（时间优先级出强分）。
	// 少了 Valid 会把 observe 档也当买入，少了 CanOpen 会把 10 点后的弱分信号拔高成必推。
	// English: buy eligibility is Valid && CanOpen — dropping either would over- or under-rate the band.
	if !res.Valid || !res.CanOpen {
		return BuyNotice{}, false
	}
	return BuyNotice{
		Code:     code,
		Name:     name,
		Stage:    res.BuyStage(),
		D1:       res.D1Event,
		Emotion:  emotion,
		IsLeader: isLeader,
		Priority: res.Priority,
		Remind:   res.RemindLevel,
		CanOpen:  res.CanOpen,
	}, true
}
