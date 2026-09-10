// bear_decision_test.go — 利空新闻分级决策器单元测试（§NEWS_BEAR）。
// 验证核心不变式：
//   - 强新闻命中 + 量价破位放量 → 利空清仓（close）；
//   - 强新闻命中但封板强势 → 利空观望（watch，封板不卖不接刀）；
//   - 中强新闻命中 + 量价中性 → 利空减仓（trim，半平留观察仓）；
//   - 新闻过弱/板块间接命中强度不足 → 观望（watch，仅提醒不动作）。
//
// English: unit tests for the §NEWS_BEAR graded bearish decision — strong news + breakdown-on-volume
// closes; strong news + limit-up watches (never sell into a sealed board); moderate news + flat trend
// trims (half off, keep a watch lot); weak or sector-indirect hits watch (reminder only).
package combat_agent

import (
	"testing"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
)

func TestDecideBearSell(t *testing.T) {
	cfg := config.DefaultBearNewsConfig()

	cases := []struct {
		name string
		hit  BearHitInfo
		q    *data.StockInfo
		want string // BearActionClose / BearActionTrim / BearActionWatch
	}{
		{
			// 强新闻直命中 + 破位放量（-3.5%、换手 8%）→ 清仓
			name: "strong news + breakdown on volume → close",
			hit:  BearHitInfo{HitLevel: BearHitStock, NewsScore: 0.9, Impact: "高", Reason: "集采利空"},
			q:    &data.StockInfo{Code: "600276", Price: 9, ChangePct: -3.5, Turnover: 8, Sector: "医药"},
			want: BearActionClose,
		},
		{
			// 强新闻直命中但封板强势（主板 10% 涨停）→ 观望（封板不卖）
			name: "strong news + limit-up → watch",
			hit:  BearHitInfo{HitLevel: BearHitStock, NewsScore: 0.95, Impact: "高", Reason: "重大利空"},
			q:    &data.StockInfo{Code: "600519", Price: 110, ChangePct: 10, Turnover: 2, Sector: "白酒"},
			want: BearActionWatch,
		},
		{
			// 中强新闻直命中 + 量价中性（-2% 无放量）→ 减仓（半平留观察仓）
			name: "moderate news + flat trend → trim",
			hit:  BearHitInfo{HitLevel: BearHitStock, NewsScore: 0.5, Impact: "中", Reason: "股东减持"},
			q:    &data.StockInfo{Code: "000001", Price: 9, ChangePct: -2, Turnover: 1, Sector: "银行"},
			want: BearActionTrim,
		},
		{
			// 新闻过弱（|score| 0.2）→ 观望（不动作）
			name: "weak news → watch",
			hit:  BearHitInfo{HitLevel: BearHitStock, NewsScore: 0.2, Impact: "低", Reason: "一般利空"},
			q:    &data.StockInfo{Code: "600001", Price: 9, ChangePct: -1, Turnover: 1, Sector: "钢铁"},
			want: BearActionWatch,
		},
		{
			// 板块间接命中（×0.55）强度不足触发减仓线 → 观望
			name: "sector-level weak → watch",
			hit:  BearHitInfo{HitLevel: BearHitSector, NewsScore: 0.4, Impact: "高", Reason: "板块利空"},
			q:    &data.StockInfo{Code: "600002", Price: 9, ChangePct: -2, Turnover: 1, Sector: "化工"},
			want: BearActionWatch,
		},
		{
			// 行情缺失（q==nil）→ 按新闻强度中性决策（不强卖不误判）
			name: "missing quote + strong news → close",
			hit:  BearHitInfo{HitLevel: BearHitStock, NewsScore: 0.9, Impact: "高", Reason: "集采利空"},
			q:    nil,
			want: BearActionClose,
		},
		{
			// 强新闻 + 大涨但未封板（+5.2% 对应 trend=+0.7）：价逆势上涨说明市场暂未买账利空，
			// 追跌清仓反而卖在阶段低点 → 观望（强趋势是对冲利空的唯一量价理由）。
			name: "strong news + strong up (not limit-up) → watch",
			hit:  BearHitInfo{HitLevel: BearHitStock, NewsScore: 0.9, Impact: "高", Reason: "重大利空"},
			q:    &data.StockInfo{Code: "600003", Price: 11, ChangePct: 5.2, Turnover: 4, Sector: "新能源"},
			want: BearActionWatch,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, name := "600000", "测试股"
			if tc.q != nil {
				code, name = tc.q.Code, tc.q.Name
			}
			plan := DecideBearSell(&tc.hit, tc.q, code, name, &cfg)
			if plan.Action != tc.want {
				t.Fatalf("DecideBearSell = %s (score=%.2f news=%.2f trend=%.2f), want %s\nreason: %s",
					plan.Action, plan.Score, plan.News, plan.Trend, tc.want, plan.Reason)
			}
			if plan.Reason == "" {
				t.Errorf("决策应带面向用户的理由")
			}
		})
	}
}

func TestDecideBearSellNilHit(t *testing.T) {
	cfg := config.DefaultBearNewsConfig()
	plan := DecideBearSell(nil, &data.StockInfo{Code: "600000", Price: 9, ChangePct: -4}, "600000", "X", &cfg)
	if plan.Action != BearActionWatch {
		t.Fatalf("nil 命中情报应观望不动作, got %s", plan.Action)
	}
}
