package server

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/store"
)

func TestOptimizationsJSONShape(t *testing.T) {
	db, _ := store.Open(filepath.Join(t.TempDir(), "t.db"))
	defer db.Close()
	_ = db.SaveOptimizationResults(990, "profitfactor", []map[string]any{
		{"rank": 1.0, "strategy": "双响炮", "strategy_kind": "",
			"params":   map[string]any{"trail_pct": 5.0, "hold_days": 20.0, "min_score": 80.0},
			"win_rate": 39.4, "profit_factor": 1.25, "win": 356.0, "loss": 548.0,
			"avg_win_pct": 7.1, "avg_loss_pct": -5.3, "avg_hold_days": 9.9, "trigger_count": 904.0},
	})
	s := &Server{researchDB: db}
	rr := httptest.NewRecorder()
	s.handleOptimizationList(rr, nil)
	var out map[string]any
	json.Unmarshal(rr.Body.Bytes(), &out)
	b, _ := json.MarshalIndent(out, "", " ")
	t.Logf("shape:\n%s", b)
}
