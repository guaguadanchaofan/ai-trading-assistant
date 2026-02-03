package riskagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/schema"
)

type Config struct {
	Enabled    bool   `yaml:"enabled"`
	Model      string `yaml:"model"`
	APIKey     string `yaml:"api_key"`
	BaseURL    string `yaml:"base_url"`
	ByAzure    bool   `yaml:"by_azure"`
	APIVersion string `yaml:"api_version"`
	TimeoutMs  int    `yaml:"timeout_ms"`
}

type EventInput struct {
	EventID     int64   `json:"event_id"`
	Type        string  `json:"type"`
	Severity    string  `json:"severity"`
	Symbol      string  `json:"symbol"`
	ChangePct   float64 `json:"change_pct,omitempty"`
	DrawdownPct float64 `json:"drawdown_pct,omitempty"`
	WindowSec   int     `json:"window_sec,omitempty"`
	Evidence    string  `json:"evidence_json,omitempty"`
}

type RiskDecision struct {
	RiskLevel  int      `json:"risk_level"`
	Severity   string   `json:"severity"`
	OneLiner   string   `json:"one_liner"`
	Why        []string `json:"why"`
	ActionHint []string `json:"action_hint"`
	Confidence float64  `json:"confidence"`
	Tags       []string `json:"tags"`
}

type Agent struct {
	enabled        bool
	model          *openai.ChatModel
	modelName      string
	disabledReason string
}

func New(cfg Config) *Agent {
	if !cfg.Enabled {
		return &Agent{enabled: false, disabledReason: "disabled by config"}
	}
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("OPENAI_API_KEY")
	}
	if cfg.Model == "" {
		cfg.Model = os.Getenv("OPENAI_MODEL")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = os.Getenv("OPENAI_BASE_URL")
	}
	if cfg.APIKey == "" || cfg.Model == "" {
		log.Printf("riskagent disabled: missing api key or model")
		return &Agent{enabled: false, disabledReason: "api_key or model missing"}
	}

	timeout := time.Duration(cfg.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	model, err := openai.NewChatModel(context.Background(), &openai.ChatModelConfig{
		APIKey:     cfg.APIKey,
		Model:      cfg.Model,
		BaseURL:    cfg.BaseURL,
		ByAzure:    cfg.ByAzure,
		APIVersion: cfg.APIVersion,
		Timeout:    timeout,
	})
	if err != nil {
		log.Printf("riskagent init error: %v", err)
		return &Agent{enabled: false, disabledReason: "init failed"}
	}

	return &Agent{enabled: true, model: model, modelName: cfg.Model}
}

func (a *Agent) Ping(ctx context.Context) (map[string]any, error) {
	if !a.enabled || a.model == nil {
		reason := a.disabledReason
		if reason == "" {
			reason = "not configured"
		}
		return map[string]any{
			"ok":     true,
			"mode":   "fallback",
			"reason": reason,
		}, nil
	}

	start := time.Now()
	messages := []*schema.Message{
		schema.SystemMessage("Return ONLY valid JSON: {\"ok\":true}. No other text."),
		schema.UserMessage("ping"),
	}
	_, err := a.model.Generate(ctx, messages)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		logLLMError(err)
		return map[string]any{
			"ok":     true,
			"mode":   "fallback",
			"reason": "llm error",
		}, err
	}
	return map[string]any{
		"ok":         true,
		"mode":       "llm",
		"model":      a.modelName,
		"latency_ms": latency,
	}, nil
}

func (a *Agent) Evaluate(ctx context.Context, in EventInput) (RiskDecision, error) {
	if !a.enabled || a.model == nil {
		return FallbackDecision(in), nil
	}

	payload, _ := json.Marshal(in)

	system := `You are RiskAgent. You MUST output ONLY valid JSON.
Rules:
- Only risk control assessment. No buy/sell points. No profit prediction.
- If evidence is insufficient or unclear, downgrade severity to low and risk_level to 1-2.
- why[] and action_hint[] must each have 1-3 concise items.
- one_liner is a single short sentence.
- confidence is 0.0-1.0.
- severity must be low|med|high.`

	messages := []*schema.Message{
		schema.SystemMessage(system),
		schema.UserMessage(fmt.Sprintf("Event: %s", string(payload))),
	}

	resp, err := a.model.Generate(ctx, messages)
	if err != nil {
		logLLMError(err)
		return fallbackFromEvent(in), err
	}
	text := strings.TrimSpace(resp.Content)

	out, err := parseRiskDecision(text)
	if err != nil {
		return fallbackFromEvent(in), err
	}
	return sanitize(out), nil
}

func FormatMarkdown(title string, decision RiskDecision) string {
	if title == "" {
		title = "Risk Decision"
	}
	lines := []string{
		fmt.Sprintf("**%s**", title),
		fmt.Sprintf("**结论**：%s (risk_level=%d, severity=%s)", decision.OneLiner, decision.RiskLevel, decision.Severity),
		"**证据**：",
	}
	for _, w := range decision.Why {
		lines = append(lines, fmt.Sprintf("- %s", w))
	}
	lines = append(lines, "**建议动作**：")
	for _, a := range decision.ActionHint {
		lines = append(lines, fmt.Sprintf("- %s", a))
	}
	lines = append(lines, fmt.Sprintf("**置信度**：%.2f", decision.Confidence))
	return strings.Join(lines, "\n")
}

func FallbackDecision(in EventInput) RiskDecision {
	return fallbackFromEvent(in)
}

func sanitize(in RiskDecision) RiskDecision {
	out := in
	if out.RiskLevel < 1 {
		out.RiskLevel = 1
	}
	if out.RiskLevel > 5 {
		out.RiskLevel = 5
	}
	out.Severity = strings.ToLower(out.Severity)
	if out.Severity != "low" && out.Severity != "med" && out.Severity != "high" {
		out.Severity = "low"
	}
	if len(out.Why) == 0 {
		out.Why = []string{"insufficient evidence"}
	}
	if len(out.Why) > 3 {
		out.Why = out.Why[:3]
	}
	if len(out.ActionHint) == 0 {
		out.ActionHint = []string{"monitor and reduce exposure risk"}
	}
	if len(out.ActionHint) > 3 {
		out.ActionHint = out.ActionHint[:3]
	}
	if out.OneLiner == "" {
		out.OneLiner = "risk assessment updated"
	}
	if out.Confidence < 0 {
		out.Confidence = 0
	}
	if out.Confidence > 1 {
		out.Confidence = 1
	}
	return out
}

func parseRiskDecision(text string) (RiskDecision, error) {
	var out RiskDecision
	if err := json.Unmarshal([]byte(text), &out); err == nil {
		return out, nil
	}
	jsonStr := extractFirstJSONObject(text)
	if jsonStr == "" {
		return RiskDecision{}, fmt.Errorf("no json object found")
	}
	if err := json.Unmarshal([]byte(jsonStr), &out); err != nil {
		return RiskDecision{}, fmt.Errorf("parse risk decision: %w", err)
	}
	return out, nil
}

func extractFirstJSONObject(s string) string {
	start := strings.Index(s, "{")
	if start == -1 {
		return ""
	}
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

func fallbackFromEvent(in EventInput) RiskDecision {
	sev := strings.ToLower(in.Severity)
	rl := 3
	conf := 0.5
	switch sev {
	case "high":
		rl = 5
		conf = 0.7
	case "med":
		rl = 3
		conf = 0.5
	default:
		sev = "low"
		rl = 1
		conf = 0.4
	}

	why, action := buildWhyAction(in)
	if len(why) == 0 {
		why = []string{"evidence provided in event payload"}
	}
	if len(action) == 0 {
		action = []string{"monitor and reduce exposure risk"}
	}

	return RiskDecision{
		RiskLevel:  rl,
		Severity:   sev,
		OneLiner:   fmt.Sprintf("%s risk assessment based on event severity", sev),
		Why:        trimList(why, 3),
		ActionHint: trimList(action, 3),
		Confidence: conf,
		Tags:       []string{strings.ToLower(in.Type), "fallback"},
	}
}

func buildWhyAction(in EventInput) ([]string, []string) {
	typ := strings.ToUpper(in.Type)
	ev := parseEvidenceMap(in.Evidence)
	threshold := getFloat(ev["threshold"])

	switch typ {
	case "PANIC_DROP":
		drawdown := in.DrawdownPct
		if drawdown > 0 {
			drawdown = -drawdown
		}
		windowSec := in.WindowSec
		mins := windowSec / 60
		if mins <= 0 {
			mins = 5
		}
		if drawdown != 0 && threshold != 0 {
			why := []string{fmt.Sprintf("%d分钟回撤 %.1f%%（阈值 -%.1f%%）", mins, drawdown, threshold)}
			action := []string{"优先减仓/收紧止损，避免加仓追涨", "等待止跌确认"}
			return why, action
		}
	case "INDEX_RISK":
		cp := in.ChangePct
		if cp > 0 {
			cp = -cp
		}
		if cp != 0 && threshold != 0 {
			why := []string{fmt.Sprintf("上证跌幅 %.1f%%（阈值 -%.1f%%），短线情绪偏弱", cp, threshold)}
			action := []string{"降低整体仓位上限", "减少高位追涨，优先防守"}
			return why, action
		}
	}
	why := buildGenericWhy(in)
	action := buildActionFromSeverity(strings.ToLower(in.Severity))
	return why, action
}

func buildGenericWhy(in EventInput) []string {
	var out []string
	if in.ChangePct != 0 {
		out = append(out, fmt.Sprintf("change_pct=%.2f", in.ChangePct))
	}
	if in.DrawdownPct != 0 {
		out = append(out, fmt.Sprintf("drawdown_pct=%.2f", in.DrawdownPct))
	}
	if in.WindowSec > 0 {
		out = append(out, fmt.Sprintf("window_sec=%d", in.WindowSec))
	}
	if in.Evidence != "" && len(out) < 3 {
		out = append(out, "evidence_json present")
	}
	return out
}

func buildActionFromSeverity(sev string) []string {
	switch sev {
	case "high":
		return []string{"reduce exposure", "tighten risk limits", "increase monitoring frequency"}
	case "med":
		return []string{"monitor closely", "review positions", "tighten stop-loss rules"}
	default:
		return []string{"monitor and collect more signals"}
	}
}

func trimList(in []string, n int) []string {
	if len(in) > n {
		return in[:n]
	}
	return in
}

func logLLMError(err error) {
	apiErr := &openai.APIError{}
	if errors.As(err, &apiErr) {
		msg := apiErr.Message
		if len(msg) > 300 {
			msg = msg[:300] + "..."
		}
		log.Printf("riskagent api error: status=%d message=%s", apiErr.HTTPStatusCode, msg)
		return
	}
	log.Printf("riskagent error: %v", err)
}

func parseEvidenceMap(s string) map[string]any {
	if s == "" {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return map[string]any{}
	}
	return m
}

func getFloat(v any) float64 {
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
	return 0
}
