package planagent

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

type Plan struct {
	MarketBias     string      `json:"market_bias"`
	MaxExposurePct float64     `json:"max_exposure_pct"`
	TradePool      []TradeItem `json:"trade_pool"`
	WatchPool      []string    `json:"watch_pool"`
	BanList        []string    `json:"ban_list"`
}

type TradeItem struct {
	Symbol      string  `json:"symbol"`
	Trigger     string  `json:"trigger"`
	Invalidate  string  `json:"invalidate"`
	PositionPct float64 `json:"position_pct"`
	StopLoss    string  `json:"stop_loss"`
}

type Input struct {
	Date   string `json:"date"`
	Quotes any    `json:"quotes"`
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
		log.Printf("planagent disabled: missing api key or model")
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
		log.Printf("planagent init error: %v", err)
		return &Agent{enabled: false, disabledReason: "init failed"}
	}

	return &Agent{enabled: true, model: model, modelName: cfg.Model}
}

func (a *Agent) Evaluate(ctx context.Context, in Input) (Plan, error) {
	if !a.enabled || a.model == nil {
		return FallbackPlan(in), nil
	}

	payload, _ := json.Marshal(in)

	system := `You are PlanAgent. Output ONLY valid JSON.
Trading style: short-term sentiment A.
Must include keys: market_bias, max_exposure_pct, trade_pool (array of {symbol,trigger,invalidate,position_pct,stop_loss}), watch_pool, ban_list.
No extra text. If uncertain, keep trade_pool empty but still output required keys.`

	messages := []*schema.Message{
		schema.SystemMessage(system),
		schema.UserMessage(fmt.Sprintf("Input: %s", string(payload))),
	}

	resp, err := a.model.Generate(ctx, messages)
	if err != nil {
		logLLMError(err)
		return FallbackPlan(in), err
	}
	text := strings.TrimSpace(resp.Content)

	plan, err := parsePlan(text)
	if err != nil {
		return FallbackPlan(in), err
	}
	return sanitizePlan(plan), nil
}

func Ping(a *Agent, ctx context.Context) (map[string]any, error) {
	if a == nil || !a.enabled || a.model == nil {
		reason := "not configured"
		if a != nil && a.disabledReason != "" {
			reason = a.disabledReason
		}
		return map[string]any{"ok": true, "mode": "fallback", "reason": reason}, nil
	}
	start := time.Now()
	messages := []*schema.Message{
		schema.SystemMessage("Return ONLY valid JSON: {\"ok\":true}."),
		schema.UserMessage("ping"),
	}
	_, err := a.model.Generate(ctx, messages)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		logLLMError(err)
		return map[string]any{"ok": true, "mode": "fallback", "reason": "llm error"}, err
	}
	return map[string]any{"ok": true, "mode": "llm", "model": a.modelName, "latency_ms": latency}, nil
}

func FallbackPlan(in Input) Plan {
	return Plan{
		MarketBias:     "neutral",
		MaxExposurePct: 30,
		TradePool:      []TradeItem{},
		WatchPool:      []string{},
		BanList:        []string{"高波动消息驱动"},
	}
}

func parsePlan(text string) (Plan, error) {
	var out Plan
	if err := json.Unmarshal([]byte(text), &out); err == nil {
		return out, nil
	}
	jsonStr := extractFirstJSONObject(text)
	if jsonStr == "" {
		return Plan{}, fmt.Errorf("no json object found")
	}
	if err := json.Unmarshal([]byte(jsonStr), &out); err != nil {
		return Plan{}, fmt.Errorf("parse plan: %w", err)
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

func sanitizePlan(p Plan) Plan {
	if p.MarketBias == "" {
		p.MarketBias = "neutral"
	}
	if p.MaxExposurePct < 0 {
		p.MaxExposurePct = 0
	}
	if p.MaxExposurePct > 100 {
		p.MaxExposurePct = 100
	}
	if p.TradePool == nil {
		p.TradePool = []TradeItem{}
	}
	if p.WatchPool == nil {
		p.WatchPool = []string{}
	}
	if p.BanList == nil {
		p.BanList = []string{}
	}
	return p
}

func logLLMError(err error) {
	apiErr := &openai.APIError{}
	if errors.As(err, &apiErr) {
		msg := apiErr.Message
		if len(msg) > 300 {
			msg = msg[:300] + "..."
		}
		log.Printf("planagent api error: status=%d message=%s", apiErr.HTTPStatusCode, msg)
		return
	}
	log.Printf("planagent error: %v", err)
}
