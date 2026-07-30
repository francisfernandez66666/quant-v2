package strategy

import "time"

type SignalType string

const (
	SignalDragon       SignalType = "dragon"
	SignalDoubleBump   SignalType = "double_bump"
	SignalNShape       SignalType = "n_shape"
	SignalDragonReturn SignalType = "dragon_return"
	SignalShortSkeleton SignalType = "short_skeleton"
)

type TradeAction string

const (
	ActionBuy   TradeAction = "buy"
	ActionSell  TradeAction = "sell"
	ActionHold  TradeAction = "hold"
	ActionWatch TradeAction = "watch"
)

type Priority int

const (
	P1   Priority = 1
	P2   Priority = 2
	P3_5 Priority = 3
	P3   Priority = 4
	P4   Priority = 5
)

type Signal struct {
	Code       string             `json:"code"`
	Name       string             `json:"name"`
	Type       SignalType         `json:"type"`
	Action     TradeAction        `json:"action"`
	Priority   Priority           `json:"priority"`
	Price      float64            `json:"price"`
	Qty        int                `json:"qty"`
	Amount     float64            `json:"amount"`
	Reason     string             `json:"reason"`
	Confidence float64            `json:"confidence"`
	Timestamp  int64              `json:"timestamp"`
	Meta       map[string]float64 `json:"meta,omitempty"`
	Reasons    map[string]string  `json:"-"`
}

type SignalResult struct {
	Signals  []Signal `json:"signals"`
	Analyzed bool     `json:"analyzed"`
}

type Evaluation struct {
	TotalScore float64            `json:"total_score"`
	Details    map[string]float64 `json:"details"`
	Pass       bool               `json:"pass"`
	Level      string             `json:"level"`
	Confidence float64            `json:"confidence"`
	Reasons    map[string]string  `json:"reasons"`
}

type ExitContext struct {
	Code      string
	Name      string
	CostPrice float64
	CurPrice  float64
	EntryAt   string
	EntryMeta map[string]float64
	DailyK    []KLine
	Now       time.Time
}

type KLine struct {
	Close  float64
	High   float64
	Low    float64
	Open   float64
	Volume float64
}

type ExitResult struct {
	Reason   string
	Priority Priority
}

type Strategy interface {
	Name() string
	Type() SignalType
	Evaluate(code string, data interface{}) (*Evaluation, error)
	GenerateSignal(code string, eval *Evaluation) (*Signal, error)
}
