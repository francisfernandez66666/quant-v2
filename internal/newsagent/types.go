package newsagent

import "time"

type NewsEvent struct {
	Title       string   `json:"title"`
	Content     string   `json:"content,omitempty"`
	Datetime    string   `json:"datetime"`
	Source      string   `json:"source"`
	IsMaterial  bool     `json:"is_material"`
	Direction   string   `json:"direction,omitempty"`
	Score       float64  `json:"score"`
	Sectors     []string `json:"sectors,omitempty"`
	Stocks      []string `json:"stocks,omitempty"`
	ImpactLevel string   `json:"impact_level,omitempty"`
	EventType   string   `json:"event_type,omitempty"`
	Urgency     string   `json:"urgency,omitempty"`
	Strategy    string   `json:"strategy,omitempty"`
	Reason      string   `json:"reason,omitempty"`
}

type AnalysisResult struct {
	Events        []NewsEvent `json:"events"`
	RawCount      int         `json:"raw_count"`
	MaterialCount int         `json:"material_count"`
	CatchUpSince  time.Time   `json:"catch_up_since"`
}

type TrackerData struct {
	SeenTitles  map[string]string `json:"seen_titles"`  // md5(title) → datetime
	LastSync    map[string]string `json:"last_sync"`    // source → latest_datetime
}
