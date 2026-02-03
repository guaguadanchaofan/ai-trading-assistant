package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"ai-trading-assistant/internal/alert"
	"ai-trading-assistant/internal/riskagent"
	"ai-trading-assistant/internal/store"
)

type Config struct {
	IndexRisk     IndexRiskConfig    `yaml:"index_risk"`
	PanicDrop     PanicDropConfig    `yaml:"panic_drop"`
	VolumeSpike   VolumeSpikeConfig  `yaml:"volume_spike"`
	KeyBreakDown  KeyBreakDownConfig `yaml:"key_break_down"`
	WindowMaxKeep int                `yaml:"window_max_keep"`
	CooldownSec   CooldownConfig     `yaml:"cooldown_sec"`
}

type IndexRiskConfig struct {
	Symbol  string  `yaml:"symbol"`
	MedPct  float64 `yaml:"med_pct"`
	HighPct float64 `yaml:"high_pct"`
}

type PanicDropConfig struct {
	WindowSec int     `yaml:"window_sec"`
	MedPct    float64 `yaml:"med_pct"`
	HighPct   float64 `yaml:"high_pct"`
}

type VolumeSpikeConfig struct {
	MaPoints int     `yaml:"ma_points"`
	Ratio    float64 `yaml:"ratio"`
}

type KeyBreakDownConfig struct {
	Levels   map[string]float64 `yaml:"levels"`
	Priority string             `yaml:"priority"` // med/high
}

type CooldownConfig struct {
	IndexRisk    int `yaml:"index_risk"`
	PanicDrop    int `yaml:"panic_drop"`
	VolumeSpike  int `yaml:"volume_spike"`
	KeyBreakDown int `yaml:"key_break_down"`
}

type Engine struct {
	cfg      Config
	store    *store.Store
	alertSvc *alert.Service
	agent    *riskagent.Agent

	mu       sync.Mutex
	windows  map[string][]store.MarketSnapshot
	cooldown map[string]int64
}

func New(cfg Config, st *store.Store, alertSvc *alert.Service, agent *riskagent.Agent) *Engine {
	if cfg.IndexRisk.Symbol == "" {
		cfg.IndexRisk.Symbol = "sh000001"
	}
	if cfg.PanicDrop.WindowSec <= 0 {
		cfg.PanicDrop.WindowSec = 300
	}
	if cfg.VolumeSpike.MaPoints <= 1 {
		cfg.VolumeSpike.MaPoints = 5
	}
	if cfg.VolumeSpike.Ratio <= 0 {
		cfg.VolumeSpike.Ratio = 3.0
	}
	if cfg.KeyBreakDown.Priority == "" {
		cfg.KeyBreakDown.Priority = "med"
	}
	if cfg.WindowMaxKeep <= 0 {
		cfg.WindowMaxKeep = 200
	}
	if cfg.CooldownSec.IndexRisk <= 0 {
		cfg.CooldownSec.IndexRisk = 300
	}
	if cfg.CooldownSec.PanicDrop <= 0 {
		cfg.CooldownSec.PanicDrop = 180
	}
	if cfg.CooldownSec.VolumeSpike <= 0 {
		cfg.CooldownSec.VolumeSpike = 180
	}
	if cfg.CooldownSec.KeyBreakDown <= 0 {
		cfg.CooldownSec.KeyBreakDown = 600
	}

	return &Engine{
		cfg:      cfg,
		store:    st,
		alertSvc: alertSvc,
		agent:    agent,
		windows:  make(map[string][]store.MarketSnapshot),
		cooldown: make(map[string]int64),
	}
}

func (e *Engine) OnSnapshot(s store.MarketSnapshot) {
	s.Symbol = strings.ToLower(strings.TrimSpace(s.Symbol))
	if s.Symbol == "" {
		return
	}
	if s.TS == 0 {
		s.TS = time.Now().Unix()
	}

	e.mu.Lock()
	window := e.windows[s.Symbol]
	window = append(window, s)
	window = e.trimWindow(window, s.TS)
	e.windows[s.Symbol] = window
	e.mu.Unlock()

	e.runRules(s, window)
}

func (e *Engine) trimWindow(window []store.MarketSnapshot, now int64) []store.MarketSnapshot {
	maxKeep := e.cfg.WindowMaxKeep
	if maxKeep > 0 && len(window) > maxKeep {
		window = window[len(window)-maxKeep:]
	}
	return window
}

func (e *Engine) runRules(s store.MarketSnapshot, window []store.MarketSnapshot) {
	e.ruleIndexRisk(s)
	e.rulePanicDrop(s, window)
	e.ruleVolumeSpike(s, window)
	e.ruleKeyBreakDown(s)
}

func (e *Engine) ruleIndexRisk(s store.MarketSnapshot) {
	if s.Symbol != strings.ToLower(e.cfg.IndexRisk.Symbol) {
		return
	}
	if s.ChangePct == 0 {
		return
	}
	if s.ChangePct <= -e.cfg.IndexRisk.HighPct {
		if !e.checkCooldown("INDEX_RISK", s.Symbol, "high", e.cfg.CooldownSec.IndexRisk) {
			return
		}
		e.emit("INDEX_RISK", "high", s, map[string]any{"change_pct": s.ChangePct, "threshold": e.cfg.IndexRisk.HighPct})
		return
	}
	if s.ChangePct <= -e.cfg.IndexRisk.MedPct {
		if !e.checkCooldown("INDEX_RISK", s.Symbol, "med", e.cfg.CooldownSec.IndexRisk) {
			return
		}
		e.emit("INDEX_RISK", "med", s, map[string]any{"change_pct": s.ChangePct, "threshold": e.cfg.IndexRisk.MedPct})
	}
}

func (e *Engine) rulePanicDrop(s store.MarketSnapshot, window []store.MarketSnapshot) {
	if !isStockSymbol(s.Symbol) {
		return
	}
	if e.cfg.PanicDrop.WindowSec <= 0 || len(window) < 2 {
		return
	}
	cutoff := s.TS - int64(e.cfg.PanicDrop.WindowSec)
	maxPrice := 0.0
	for i := len(window) - 1; i >= 0; i-- {
		if window[i].TS < cutoff {
			break
		}
		if window[i].Price > maxPrice {
			maxPrice = window[i].Price
		}
	}
	if maxPrice <= 0 {
		return
	}
	drawdownPct := (s.Price - maxPrice) / maxPrice * 100
	if drawdownPct <= -e.cfg.PanicDrop.HighPct {
		if !e.checkCooldown("PANIC_DROP", s.Symbol, "high", e.cfg.CooldownSec.PanicDrop) {
			return
		}
		e.emit("PANIC_DROP", "high", s, map[string]any{"drawdown_pct": drawdownPct, "window_sec": e.cfg.PanicDrop.WindowSec, "threshold": e.cfg.PanicDrop.HighPct})
		return
	}
	if drawdownPct <= -e.cfg.PanicDrop.MedPct {
		if !e.checkCooldown("PANIC_DROP", s.Symbol, "med", e.cfg.CooldownSec.PanicDrop) {
			return
		}
		e.emit("PANIC_DROP", "med", s, map[string]any{"drawdown_pct": drawdownPct, "window_sec": e.cfg.PanicDrop.WindowSec, "threshold": e.cfg.PanicDrop.MedPct})
	}
}

func (e *Engine) ruleVolumeSpike(s store.MarketSnapshot, window []store.MarketSnapshot) {
	if !isStockSymbol(s.Symbol) {
		return
	}
	if e.cfg.VolumeSpike.MaPoints <= 1 || len(window) < e.cfg.VolumeSpike.MaPoints {
		return
	}
	start := len(window) - e.cfg.VolumeSpike.MaPoints
	if start < 0 {
		start = 0
	}
	var sum float64
	var count int
	for i := start; i < len(window)-1; i++ { // exclude current
		if window[i].Volume > 0 {
			sum += window[i].Volume
			count++
		}
	}
	if count == 0 {
		return
	}
	avg := sum / float64(count)
	if avg <= 0 {
		return
	}
	ratio := s.Volume / avg
	if ratio >= e.cfg.VolumeSpike.Ratio {
		if !e.checkCooldown("VOLUME_SPIKE", s.Symbol, "med", e.cfg.CooldownSec.VolumeSpike) {
			return
		}
		e.emit("VOLUME_SPIKE", "med", s, map[string]any{"ratio": ratio, "avg": avg})
	}
}

func (e *Engine) ruleKeyBreakDown(s store.MarketSnapshot) {
	if !isStockSymbol(s.Symbol) {
		return
	}
	if len(e.cfg.KeyBreakDown.Levels) == 0 {
		return
	}
	level, ok := e.cfg.KeyBreakDown.Levels[s.Symbol]
	if !ok {
		return
	}
	if s.Price <= 0 {
		return
	}
	if s.Price < level {
		severity := strings.ToLower(e.cfg.KeyBreakDown.Priority)
		if severity != "high" {
			severity = "med"
		}
		if !e.checkCooldown("KEY_BREAK_DOWN", s.Symbol, severity, e.cfg.CooldownSec.KeyBreakDown) {
			return
		}
		e.emit("KEY_BREAK_DOWN", severity, s, map[string]any{"level": level})
	}
}

func (e *Engine) emit(eventType string, severity string, s store.MarketSnapshot, evidence map[string]any) {
	if e.store == nil {
		log.Printf("event store not configured, drop event=%s", eventType)
		return
	}
	if severity == "" {
		severity = "med"
	}

	windowTag := ""
	if v, ok := evidence["window_sec"]; ok {
		windowTag = fmt.Sprintf("w%v", v)
	}
	if v, ok := evidence["level"]; ok && windowTag == "" {
		windowTag = fmt.Sprintf("lvl%v", v)
	}
	if v, ok := evidence["threshold"]; ok && windowTag == "" {
		windowTag = fmt.Sprintf("thr%v", v)
	}
	if windowTag == "" {
		windowTag = "base"
	}
	dedupKey := fmt.Sprintf("%s:%s:%s:%s", eventType, s.Symbol, windowTag, severity)
	mergeKey := fmt.Sprintf("risk:%s", s.Symbol)

	evidenceJSON, _ := json.Marshal(evidence)
	evt := store.EventRecord{
		TS:           s.TS,
		Type:         eventType,
		Severity:     severity,
		GroupName:    "risk",
		Title:        buildEventTitle(eventType, s, evidence),
		DedupKey:     dedupKey,
		MergeKey:     mergeKey,
		EvidenceJSON: string(evidenceJSON),
	}
	eventID, err := e.store.InsertEventReturnID(evt)
	if err != nil {
		log.Printf("insert event error: %v", err)
	}

	if e.alertSvc == nil {
		return
	}

	decision, err := e.evaluateRisk(eventID, evt, s, evidence)
	if err != nil {
		log.Printf("riskagent evaluate error: %v", err)
	}
	priority := alert.Priority(strings.ToLower(decision.Severity))
	if priority != alert.PriorityHigh && priority != alert.PriorityMed {
		priority = alert.PriorityLow
	}
	markdown := riskagent.FormatMarkdown(evt.Title, decision)
	alertReq := alert.AlertRequest{
		Priority: priority,
		Group:    "risk",
		Title:    evt.Title,
		Markdown: markdown,
		DedupKey: dedupKey,
		MergeKey: mergeKey,
	}
	res := e.alertSvc.Handle(context.Background(), alertReq)
	if res.Error != nil {
		log.Printf("alert handle error: %v", res.Error)
	}
}

func buildMarkdown(eventType string, s store.MarketSnapshot, evidence map[string]any) string {
	lines := []string{
		fmt.Sprintf("**%s**", eventType),
		fmt.Sprintf("- symbol: %s", s.Symbol),
		fmt.Sprintf("- price: %.4f", s.Price),
		fmt.Sprintf("- change_pct: %.4f", s.ChangePct),
		fmt.Sprintf("- volume: %.0f", s.Volume),
	}
	keys := make([]string, 0, len(evidence))
	for k := range evidence {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		lines = append(lines, fmt.Sprintf("- %s: %v", k, evidence[k]))
	}
	return strings.Join(lines, "\n")
}

func (e *Engine) evaluateRisk(eventID int64, evt store.EventRecord, s store.MarketSnapshot, evidence map[string]any) (riskagent.RiskDecision, error) {
	drawdown := getFloat(evidence, "drawdown_pct")
	windowSec := getInt(evidence, "window_sec")
	input := riskagent.EventInput{
		EventID:     eventID,
		Type:        evt.Type,
		Severity:    evt.Severity,
		Symbol:      s.Symbol,
		ChangePct:   s.ChangePct,
		DrawdownPct: drawdown,
		WindowSec:   windowSec,
		Evidence:    evt.EvidenceJSON,
	}
	if e.agent == nil {
		return riskagent.FallbackDecision(input), nil
	}
	return e.agent.Evaluate(context.Background(), input)
}

func getFloat(m map[string]any, key string) float64 {
	if v, ok := m[key]; ok {
		switch t := v.(type) {
		case float64:
			return t
		case float32:
			return float64(t)
		case int:
			return float64(t)
		case int64:
			return float64(t)
		}
	}
	return 0
}

func getInt(m map[string]any, key string) int {
	if v, ok := m[key]; ok {
		switch t := v.(type) {
		case int:
			return t
		case int64:
			return int(t)
		case float64:
			return int(t)
		}
	}
	return 0
}

func buildEventTitle(eventType string, s store.MarketSnapshot, evidence map[string]any) string {
	switch eventType {
	case "INDEX_RISK":
		return fmt.Sprintf("%s INDEX_RISK change_pct=%.2f", s.Symbol, s.ChangePct)
	case "PANIC_DROP":
		if v, ok := evidence["drawdown_pct"]; ok {
			if w, ok := evidence["window_sec"]; ok {
				return fmt.Sprintf("%s PANIC_DROP drawdown=%v window_sec=%v", s.Symbol, v, w)
			}
			return fmt.Sprintf("%s PANIC_DROP drawdown=%v", s.Symbol, v)
		}
	}
	return fmt.Sprintf("%s %s", s.Symbol, eventType)
}

func isStockSymbol(sym string) bool {
	s := strings.ToLower(sym)
	if len(s) != 8 {
		return false
	}
	if !strings.HasPrefix(s, "sh") && !strings.HasPrefix(s, "sz") {
		return false
	}
	code := s[2:]
	for i := 0; i < len(code); i++ {
		if code[i] < '0' || code[i] > '9' {
			return false
		}
	}
	return s != "sh000001"
}

func (e *Engine) checkCooldown(ruleType, symbol, severity string, cooldownSec int) bool {
	if cooldownSec <= 0 {
		return true
	}
	key := fmt.Sprintf("%s:%s:%s", ruleType, symbol, severity)
	now := time.Now().Unix()
	e.mu.Lock()
	defer e.mu.Unlock()
	if last, ok := e.cooldown[key]; ok {
		if now-last < int64(cooldownSec) {
			return false
		}
	}
	e.cooldown[key] = now
	return true
}
