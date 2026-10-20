// §F-18 HealthCheck 并发 + 总时限：一条挂起的腿不得把整个健康接口拖成串行等待。
package data

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

type blockingRT struct{ gate <-chan struct{} }

func (b blockingRT) RoundTrip(*http.Request) (*http.Response, error) {
	<-b.gate
	return nil, errors.New("blocked by test")
}

func TestHealthCheckBoundedByTimeout(t *testing.T) {
	old := healthCheckTimeout
	healthCheckTimeout = 150 * time.Millisecond
	defer func() { healthCheckTimeout = old }()

	gate := make(chan struct{})
	defer close(gate)
	m := NewMarketAPI()
	m.SetTransport(blockingRT{gate: gate})

	start := time.Now()
	res := m.HealthCheck()
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("HealthCheck 耗时 %s，未按总时限收口", elapsed)
	}
	for name, ok := range res {
		if ok {
			t.Fatalf("%s 在挂起时应判不可用", name)
		}
	}
}
