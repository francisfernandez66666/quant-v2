// Package data — 令牌桶限流器。
// 按数据源分别定义限流速率（东财 3/s、新浪 30/s、同花顺 8/s 等），
// 所有对外 API 请求前调用 Wait() 阻塞取令牌，防止触发反爬封禁。
package data

import (
	"log"
	"sync"
	"time"
)

// RateLimiter 令牌桶限流器。
type RateLimiter struct {
	mu     sync.Mutex // 保护令牌计数
	tokens float64    // 当前可用令牌数
	rate   float64    // 每秒补充速率
	burst  float64    // 桶容量（最大突发）
	last   time.Time  // 上次补充时间
	name   string     // 限流器名称（日志标识）
}

// NewRateLimiter 创建指定速率的限流器。
func NewRateLimiter(name string, ratePerSec, burst float64) *RateLimiter {
	return &RateLimiter{
		tokens: burst,
		rate:   ratePerSec,
		burst:  burst,
		last:   time.Now(),
		name:   name,
	}
}

// DisableAll 置 true 时所有限流器 Wait() 直接返回。
// 仅供测试使用（e2e/mock 网络下避免被限流拖慢）。
var DisableAll bool

// Wait 获取一个令牌，不足时阻塞等待。
func (rl *RateLimiter) Wait() {
	if DisableAll {
		return
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(rl.last).Seconds()
	rl.last = now
	// 按流逝时间补充令牌，超过桶容量则截断
	rl.tokens += elapsed * rl.rate
	if rl.tokens > rl.burst {
		rl.tokens = rl.burst
	}
	if rl.tokens < 1 {
		// 令牌不足：计算缺口所需等待时间并阻塞，随后清零
		wait := time.Duration((1 - rl.tokens) / rl.rate * float64(time.Second))
		log.Printf("[rate] %s 限流等待 %v", rl.name, wait)
		time.Sleep(wait)
		rl.tokens = 0
	} else {
		rl.tokens--
	}
}

var (
	TushareLimiter   = NewRateLimiter("tushare", 2, 6)   // 120/min官方限制，2/s均值，允许6突发
	SinaLimiter      = NewRateLimiter("sina", 30, 60)    // 30/s，60突发
	EastMoneyLimiter = NewRateLimiter("eastmoney", 3, 5) // 3/s，5突发
	THSLimiter       = NewRateLimiter("ths", 8, 12)      // 8/s，12突发
	CLSLimiter       = NewRateLimiter("cls", 3, 5)       // 3/s，5突发
	YahooLimiter     = NewRateLimiter("yahoo", 5, 8)
)
