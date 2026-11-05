package strategy

// derive_carrier_test.go —— 载体侧 rule_side 与四枚 *_kind 键的真源就是这里的命名常量。
//
// 为什么单独上锁：这四枚字符串以前只在**战法包的 Name()** 里出现一次，
// 而 Name() 与 Signal* 常量是两处写法；导出工具取的是常量那一处。
// 谁把 SignalBreakDown 的值改了（比如重命名成 breakdown），载体的 signal_codes 表、
// RULES 注册表和 Go 的战法名会当场分成三套——这套用例把"常量值即对外契约"钉住。

import "testing"

// TestBearSignalStringsAreTheContract 做空四战法的信号种类取值（导出到载体的那四枚）。
func TestBearSignalStringsAreTheContract(t *testing.T) {
	want := map[SignalType]string{
		SignalBreakDown:    "break_down",
		SignalHighChurn:    "high_churn",
		SignalLeaderDecay:  "leader_decay",
		SignalGoodNewsFade: "good_news_fade",
	}
	for got, expected := range want {
		if string(got) != expected {
			t.Errorf("信号种类常量漂了：%s 应为 %q（载体的 signal_codes 与 RULES 注册表都按这个名字对表）",
				got, expected)
		}
	}
	// 四个种类两两不同：共用一枚就等于两条规则抢同一个备注短码。
	seen := map[string]bool{}
	for kind := range want {
		if seen[string(kind)] {
			t.Errorf("信号种类重复：%s", kind)
		}
		seen[string(kind)] = true
	}
}

// TestSellSideIsTheBearDirection 四条做空战法的出信号方向都取 ActionSell。
// 载体侧那枚 rule_side 是"这条规则能发什么方向"的唯一读数，它一旦与战法实际发的方向不一致，
// 沙箱就会把卖出意图按买入装配（金额与持仓两侧同时错）。
func TestSellSideIsTheBearDirection(t *testing.T) {
	if string(ActionSell) != "sell" {
		t.Errorf("ActionSell=%q：载体按这个字符串判方向", ActionSell)
	}
	if ActionBuy == ActionSell {
		t.Error("买与卖两个方向不能同值")
	}
}
