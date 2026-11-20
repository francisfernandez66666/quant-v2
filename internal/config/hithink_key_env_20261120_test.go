// hithink_key_env_20261120_test.go — §HITHINK-KEY-SINGLE（owner 2026-11-20）。
//
// owner 的原话是「hithink key 我在自动研究 tab-设置子 tab 里设置过，你把这里的 key 当全部的
// hithink 设置」。现场问题：那个 key 只被用于**拉起 hithink-sync 子进程**时的环境变量注入
// （scheduler/worker.go），而主进程里的 `data.NewHithinkClient()` 只读 `os.Getenv` ——
// 于是运营在 UI 里配好了 key，行情第一顺位/涨停池/资金流快照/交易日历这几条腿仍然报
// 「环境变量 HITHINK_FINANCE_API_KEY 未设置」，表现为"配了没用、必须重启也没用"。
//
// 修法：保存（与启动装配）时把配置值发布到进程环境。本文件钉三件事：
//  1. 保存即生效：SetSchedulerHithinkKey 之后 os.Getenv 立刻是新值；
//  2. 撤销要清干净：保存空串必须 Unsetenv，否则撤销后旧 key 仍被主进程持有；
//  3. 键名与 hithinkKeyEnvForTest 同值（config 不 import data 以避免包循环，所以靠锁保证）。
package config

import (
	"os"
	"path/filepath"
	"testing"

)

// hithinkKeyEnvForTest 与 data.HithinkAPIKeyEnv 同值。
// 为什么不直接 import data：`data` 反向依赖 `config`，同包测试 import 会造成包循环
// （实测 `imports quant-trading-v2/internal/data ... [setup failed]`）。
// 键名的一致性由门禁锁（§234 里两处字符串逐字比对），不靠类型系统。
const hithinkKeyEnvForTest = "HITHINK_FINANCE_API_KEY"

// TestSetSchedulerHithinkKeyPublishesToProcessEnv 保存即生效 + 撤销即清除。
func TestSetSchedulerHithinkKeyPublishesToProcessEnv(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "config.json"))
	// 隔离：用例结束时恢复现场（别的用例可能依赖真实 env）。
	prev, had := os.LookupEnv(hithinkKeyEnvForTest)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(hithinkKeyEnvForTest, prev)
		} else {
			_ = os.Unsetenv(hithinkKeyEnvForTest)
		}
	})
	_ = os.Unsetenv(hithinkKeyEnvForTest)

	if err := m.SetSchedulerHithinkKey("u_1", "ths-key-abc"); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if got := os.Getenv(hithinkKeyEnvForTest); got != "ths-key-abc" {
		t.Fatalf("保存后进程环境未更新：got %q", got)
	}
	// 撤销必须把环境变量也清掉（否则主进程仍拿着旧 key，用户以为撤了其实没撤）。
	if err := m.SetSchedulerHithinkKey("u_1", ""); err != nil {
		t.Fatalf("撤销失败: %v", err)
	}
	if got, ok := os.LookupEnv(hithinkKeyEnvForTest); ok && got != "" {
		t.Fatalf("撤销后环境变量应被清除，got %q", got)
	}
}

// TestHithinkKeyEnvNameIsSingleSourced 键名必须与 data 包常量一致（两处各写一份必然会漂）。
func TestHithinkKeyEnvNameIsSingleSourced(t *testing.T) {
	if hithinkKeyEnvForTest != "HITHINK_FINANCE_API_KEY" {
		t.Fatalf("data 包的键名变了：%q（config 里那份硬编码也要同批改）", hithinkKeyEnvForTest)
	}
}
