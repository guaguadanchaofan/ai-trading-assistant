package main

import (
	"fmt"
	"log"
	"time"

	"ai-trading-assistant/internal/alert"
	"ai-trading-assistant/internal/api"
	"ai-trading-assistant/internal/config"
	"ai-trading-assistant/internal/engine"
	"ai-trading-assistant/internal/market"
	"ai-trading-assistant/internal/planagent"
	"ai-trading-assistant/internal/push/dingtalk"
	"ai-trading-assistant/internal/riskagent"
	"ai-trading-assistant/internal/store"

	"github.com/cloudwego/hertz/pkg/app/server"
)

func main() {
	cfg, err := config.Load("configs/app.yaml")
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	h := server.Default(server.WithHostPorts(addr))

	dt := dingtalk.NewClient(
		cfg.Push.Dingtalk.Webhook,
		cfg.Push.Dingtalk.Secret,
		time.Duration(cfg.Push.Dingtalk.TimeoutMs)*time.Millisecond,
	)

	st, err := store.Open(cfg.Store.Sqlite.Path)
	if err != nil {
		log.Fatalf("store error: %v", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			log.Printf("store close error: %v", err)
		}
	}()

	alertSvc := alert.NewService(dt, st, alert.Config{
		RateLimit: alert.RateLimitConfig{
			PerMinute: cfg.Alert.RateLimit.PerMinute,
			Burst:     cfg.Alert.RateLimit.Burst,
		},
		DedupWindow:       time.Duration(cfg.Alert.Dedup.WindowSec) * time.Second,
		MergeWindow:       time.Duration(cfg.Alert.Merge.WindowSec) * time.Second,
		LowDigestInterval: time.Duration(cfg.Alert.Digest.LowIntervalSec) * time.Second,
	})

	var agent *riskagent.Agent
	if cfg.RiskAgent.Enabled {
		agent = riskagent.New(riskagent.Config{
			Enabled:    cfg.RiskAgent.Enabled,
			Model:      cfg.RiskAgent.Model,
			APIKey:     cfg.RiskAgent.APIKey,
			BaseURL:    cfg.RiskAgent.BaseURL,
			ByAzure:    cfg.RiskAgent.ByAzure,
			APIVersion: cfg.RiskAgent.APIVersion,
			TimeoutMs:  cfg.RiskAgent.TimeoutMs,
		})
	}

	planAgent := planagent.New(planagent.Config{
		Enabled:    cfg.PlanAgent.Enabled,
		Model:      cfg.PlanAgent.Model,
		APIKey:     cfg.PlanAgent.APIKey,
		BaseURL:    cfg.PlanAgent.BaseURL,
		ByAzure:    cfg.PlanAgent.ByAzure,
		APIVersion: cfg.PlanAgent.APIVersion,
		TimeoutMs:  cfg.PlanAgent.TimeoutMs,
	})

	eng := engine.New(engine.Config{
		IndexRisk: engine.IndexRiskConfig{
			Symbol:  cfg.Engine.IndexRisk.Symbol,
			MedPct:  cfg.Engine.IndexRisk.MedPct,
			HighPct: cfg.Engine.IndexRisk.HighPct,
		},
		PanicDrop: engine.PanicDropConfig{
			WindowSec: cfg.Engine.PanicDrop.WindowSec,
			MedPct:    cfg.Engine.PanicDrop.MedPct,
			HighPct:   cfg.Engine.PanicDrop.HighPct,
		},
		VolumeSpike: engine.VolumeSpikeConfig{
			MaPoints: cfg.Engine.VolumeSpike.MaPoints,
			Ratio:    cfg.Engine.VolumeSpike.Ratio,
		},
		KeyBreakDown: engine.KeyBreakDownConfig{
			Levels:   cfg.Engine.KeyBreakDown.Levels,
			Priority: cfg.Engine.KeyBreakDown.Priority,
		},
		CooldownSec: engine.CooldownConfig{
			IndexRisk:    cfg.Engine.CooldownSec.IndexRisk,
			PanicDrop:    cfg.Engine.CooldownSec.PanicDrop,
			VolumeSpike:  cfg.Engine.CooldownSec.VolumeSpike,
			KeyBreakDown: cfg.Engine.CooldownSec.KeyBreakDown,
		},
		WindowMaxKeep: cfg.Engine.WindowMaxKeep,
	}, st, alertSvc, agent)

	mktProvider := market.NewMultiProvider(
		market.NewEastmoneyProvider(5*time.Second),
		market.NewSinaProvider(5*time.Second),
	)
	mktSvc := market.NewService(mktProvider, time.Duration(cfg.Market.MinRequestIntervalMs)*time.Millisecond, st, eng)

	if cfg.Market.PollIntervalSec > 0 && len(cfg.Market.Symbols) > 0 {
		go func() {
			mktSvc.PollLoop(cfg.Market.Symbols, time.Duration(cfg.Market.PollIntervalSec)*time.Second)
		}()
	}

	api.RegisterRoutes(h, dt, alertSvc, st, mktSvc, cfg.Market.Symbols, eng, agent, planAgent)
	log.Printf("route registered: POST /api/v1/test/risk/ping")
	log.Printf("route registered: POST /api/v1/test/risk/eval")

	log.Printf("server starting on %s (log.level=%s)", addr, cfg.Log.Level)
	if err := h.Run(); err != nil {
		log.Fatalf("server run error: %v", err)
	}
}
