package data

import (
	"log"
	"sync"
	"time"
)

type RateLimiter struct {
	mu     sync.Mutex
	tokens float64
	rate   float64
	burst  float64
	last   time.Time
	name   string
}

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

func (rl *RateLimiter) Wait() {
	if DisableAll {
		return
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(rl.last).Seconds()
	rl.last = now
	rl.tokens += elapsed * rl.rate
	if rl.tokens > rl.burst {
		rl.tokens = rl.burst
	}
	if rl.tokens < 1 {
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
