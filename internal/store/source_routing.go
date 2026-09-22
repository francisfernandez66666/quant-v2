// 复权/行情数据源路由的唯一装配入口（§P0-A 三轮补强·范围盲区）。
//
// 背景：PrimarySourceThsDaily / ThsFactorsReady 曾是**包级导出变量**，装配点只有
// cmd/quant 与 internal/scheduler 两处，其余同样读 HfqBars 的 main（cmd/replay、
// cmd/research、cmd/dataload …）全部不装配 → 同一支票同一区间，手工回测与引擎打分
// 走两套复权体系。本文件把这两个开关收为**包内私有 + 单一写入口**：
//   - 任何进程只能经 ConfigureSource / ConfigureSourceFromFile 装配，物理上无法
//     "各 main 自己 set 包级变量"（变量已不导出）。
//   - ConfigureSource 统一 "hithink" 大小写匹配语义（此前 cmd/quant 用 strings.EqualFold、
//     scheduler 亦用 EqualFold，两处重复），并打印生效日志供排障比对。
//   - ConfigureSourceFromFile 供【手上没有配置对象】的 cmd 使用：直接读 config.json 的
//     rules.data 段。这里刻意不 import internal/config（保持存储层零配置依赖，键名与
//     internal/config.DataConfig 对齐，注释处已标注），只取路由两字段。
//
// （Single assembly entry for the data-source routing switches: the two flags are now
// package-private and writable only through ConfigureSource / ConfigureSourceFromFile, so every
// binary that reads HfqBars/RawBars resolves the same adjustment system.）
package store

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
)

// routingMu 保护两个路由开关：scheduler 每 tick 热重载（写），查询协程并发读。
var routingMu sync.RWMutex

// primarySourceThsDaily 数据源路由开关：true 时 RawBars 优先读 ths_daily（同花顺（新）），
// 该股无数据回退旧 daily 表。由 config rules.data.primary_source 经 ConfigureSource 装配。
var primarySourceThsDaily = false

// thsFactorsReady 同花顺复权因子对账门禁：true 时 HfqBars 走 ths_daily×ths_adj_factor；
// false（默认）时 hfq 仍走旧表——门禁未过前禁止消费（docs/HITHINK_DATA_SOURCE_PLAN §6.3）。
var thsFactorsReady = false

// ConfigureSource 数据源路由唯一写入口。primarySource 取 config 的 rules.data.primary_source
// （"hithink"=同花顺新表优先，其余值/空=旧 baostock 表）；factorsReady 为复权对账门禁。
// 幂等：与当前值相同时不重复打日志。
// （ConfigureSource is the only writer of the routing switches.）
func ConfigureSource(primarySource string, factorsReady bool) {
	ths := strings.EqualFold(strings.TrimSpace(primarySource), "hithink")
	routingMu.Lock()
	changed := ths != primarySourceThsDaily || factorsReady != thsFactorsReady
	primarySourceThsDaily, thsFactorsReady = ths, factorsReady
	routingMu.Unlock()
	if changed {
		log.Printf("[store] 数据源路由装配：primary_source=%q → ths_daily=%v ths_factors_ready=%v",
			primarySource, ths, factorsReady)
	}
}

// ConfigureSourceFromFile 从 config.json 读 rules.data 段装配路由——供【没有配置对象】的
// cmd（replay / research / dataload 等只拿到 -db/-config 的入口）统一走同一入口。
// cfgPath 为空时回退 DefaultConfigPath()；文件缺失/解析失败 → 保持出厂默认（旧表）并告警，
// 绝不静默用一套未声明的口径。
// （ConfigureSourceFromFile reads rules.data straight from config.json so every binary shares
// one routing entry point; a missing file keeps the safe default and logs.）
func ConfigureSourceFromFile(cfgPath string) error {
	path := strings.TrimSpace(cfgPath)
	if path == "" {
		path = DefaultConfigPath()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		// 读不到配置：显式装配为出厂默认（baostock 旧表 + 门禁关），避免继承上一进程的残留态。
		ConfigureSource("", false)
		log.Printf("[store] 数据源配置 %s 读取失败，按默认（旧表 baostock）装配: %v", path, err)
		return fmt.Errorf("store: 读取数据源配置 %s 失败（已按默认 baostock 旧表装配）: %w", path, err)
	}
	var wrapper struct {
		Rules struct {
			Data struct {
				PrimarySource   string `json:"primary_source"`    // 与 internal/config.DataConfig 同键
				ThsFactorsReady bool   `json:"ths_factors_ready"` // 与 internal/config.DataConfig 同键
			} `json:"data"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		ConfigureSource("", false)
		return fmt.Errorf("store: 解析数据源配置 %s 失败（按默认 baostock 旧表装配）: %w", path, err)
	}
	ConfigureSource(wrapper.Rules.Data.PrimarySource, wrapper.Rules.Data.ThsFactorsReady)
	return nil
}

// DefaultConfigPath 缺省配置路径：QUANT_DATA_DIR 优先，否则 $HOME/.quant-trading-v2/config.json
// （与仓内各 main 的 dataDir 约定一致：config.json 与 trading.db 同目录）。
func DefaultConfigPath() string {
	dir := os.Getenv("QUANT_DATA_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			dir = ".quant-trading-v2"
		} else {
			dir = home + "/.quant-trading-v2"
		}
	}
	return dir + "/config.json"
}

// CurrentSource 返回当前生效的路由（诊断/测试用）。
func CurrentSource() (primaryThsDaily bool, factorsReady bool) {
	routingMu.RLock()
	defer routingMu.RUnlock()
	return primarySourceThsDaily, thsFactorsReady
}

// useThsDaily / useThsHfq 查询侧的带锁读取（HfqBars、RawBars 用）。
func useThsDaily() bool {
	routingMu.RLock()
	defer routingMu.RUnlock()
	return primarySourceThsDaily
}

func useThsHfq() bool {
	routingMu.RLock()
	defer routingMu.RUnlock()
	return primarySourceThsDaily && thsFactorsReady
}
