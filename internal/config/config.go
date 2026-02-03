package config

import (
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Log       LogConfig       `yaml:"log"`
	Push      PushConfig      `yaml:"push"`
	Alert     AlertConfig     `yaml:"alert"`
	Store     StoreConfig     `yaml:"store"`
	Market    MarketConfig    `yaml:"market"`
	Engine    EngineConfig    `yaml:"engine"`
	RiskAgent RiskAgentConfig `yaml:"risk_agent"`
	PlanAgent PlanAgentConfig `yaml:"plan_agent"`
}

type ServerConfig struct {
	Port int `yaml:"port"`
}

type LogConfig struct {
	Level string `yaml:"level"`
}

type PushConfig struct {
	Dingtalk DingtalkConfig `yaml:"dingtalk"`
}

type DingtalkConfig struct {
	Webhook   string `yaml:"webhook"`
	Secret    string `yaml:"secret"`
	TimeoutMs int    `yaml:"timeout_ms"`
}

type AlertConfig struct {
	RateLimit RateLimitConfig `yaml:"rate_limit"`
	Dedup     DedupConfig     `yaml:"dedup"`
	Merge     MergeConfig     `yaml:"merge"`
	Digest    DigestConfig    `yaml:"digest"`
}

type RateLimitConfig struct {
	PerMinute int `yaml:"per_minute"`
	Burst     int `yaml:"burst"`
}

type DedupConfig struct {
	WindowSec int `yaml:"window_sec"`
}

type MergeConfig struct {
	WindowSec int `yaml:"window_sec"`
}

type DigestConfig struct {
	LowIntervalSec int `yaml:"low_interval_sec"`
}

type StoreConfig struct {
	Sqlite SqliteConfig `yaml:"sqlite"`
}

type SqliteConfig struct {
	Path string `yaml:"path"`
}

type MarketConfig struct {
	Symbols              []string `yaml:"symbols"`
	PollIntervalSec      int      `yaml:"poll_interval_sec"`
	MinRequestIntervalMs int      `yaml:"min_request_interval_ms"`
}

type EngineConfig struct {
	IndexRisk     EngineIndexRiskConfig    `yaml:"index_risk"`
	PanicDrop     EnginePanicDropConfig    `yaml:"panic_drop"`
	VolumeSpike   EngineVolumeSpikeConfig  `yaml:"volume_spike"`
	KeyBreakDown  EngineKeyBreakDownConfig `yaml:"key_break_down"`
	WindowMaxKeep int                      `yaml:"window_max_keep"`
	CooldownSec   EngineCooldownConfig     `yaml:"cooldown_sec"`
}

type EngineIndexRiskConfig struct {
	Symbol  string  `yaml:"symbol"`
	MedPct  float64 `yaml:"med_pct"`
	HighPct float64 `yaml:"high_pct"`
}

type EnginePanicDropConfig struct {
	WindowSec int     `yaml:"window_sec"`
	MedPct    float64 `yaml:"med_pct"`
	HighPct   float64 `yaml:"high_pct"`
}

type EngineVolumeSpikeConfig struct {
	MaPoints int     `yaml:"ma_points"`
	Ratio    float64 `yaml:"ratio"`
}

type EngineKeyBreakDownConfig struct {
	Levels   map[string]float64 `yaml:"levels"`
	Priority string             `yaml:"priority"`
}

type EngineCooldownConfig struct {
	IndexRisk    int `yaml:"index_risk"`
	PanicDrop    int `yaml:"panic_drop"`
	VolumeSpike  int `yaml:"volume_spike"`
	KeyBreakDown int `yaml:"key_break_down"`
}

type RiskAgentConfig struct {
	Enabled    bool   `yaml:"enabled"`
	Model      string `yaml:"model"`
	APIKey     string `yaml:"api_key"`
	BaseURL    string `yaml:"base_url"`
	ByAzure    bool   `yaml:"by_azure"`
	APIVersion string `yaml:"api_version"`
	TimeoutMs  int    `yaml:"timeout_ms"`
}

type PlanAgentConfig struct {
	Enabled    bool   `yaml:"enabled"`
	Model      string `yaml:"model"`
	APIKey     string `yaml:"api_key"`
	BaseURL    string `yaml:"base_url"`
	ByAzure    bool   `yaml:"by_azure"`
	APIVersion string `yaml:"api_version"`
	TimeoutMs  int    `yaml:"timeout_ms"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := Config{
		Server: ServerConfig{Port: 8080},
		Log:    LogConfig{Level: "info"},
		Push: PushConfig{
			Dingtalk: DingtalkConfig{TimeoutMs: 5000},
		},
		Alert: AlertConfig{
			RateLimit: RateLimitConfig{PerMinute: 60, Burst: 10},
			Dedup:     DedupConfig{WindowSec: 60},
			Merge:     MergeConfig{WindowSec: 30},
			Digest:    DigestConfig{LowIntervalSec: 60},
		},
		Store: StoreConfig{
			Sqlite: SqliteConfig{Path: "data/app.db"},
		},
		Market: MarketConfig{
			Symbols:              []string{"sh000001", "sh600000", "sz000001"},
			PollIntervalSec:      30,
			MinRequestIntervalMs: 1000,
		},
		Engine: EngineConfig{
			IndexRisk: EngineIndexRiskConfig{
				Symbol:  "sh000001",
				MedPct:  1.5,
				HighPct: 3.0,
			},
			PanicDrop: EnginePanicDropConfig{
				WindowSec: 300,
				MedPct:    2.0,
				HighPct:   4.0,
			},
			VolumeSpike: EngineVolumeSpikeConfig{
				MaPoints: 5,
				Ratio:    3.0,
			},
			KeyBreakDown: EngineKeyBreakDownConfig{
				Levels: map[string]float64{
					"sh000001": 2800,
				},
				Priority: "med",
			},
			WindowMaxKeep: 200,
			CooldownSec: EngineCooldownConfig{
				IndexRisk:    300,
				PanicDrop:    180,
				VolumeSpike:  180,
				KeyBreakDown: 600,
			},
		},
		RiskAgent: RiskAgentConfig{
			Enabled:   false,
			Model:     "gpt-4.1-mini",
			TimeoutMs: 10000,
		},
		PlanAgent: PlanAgentConfig{
			Enabled:   false,
			Model:     "gpt-4.1-mini",
			TimeoutMs: 10000,
		},
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if err := applyEnvOverrides(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func applyEnvOverrides(cfg *Config) error {
	if v := os.Getenv("PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p <= 0 || p > 65535 {
			return fmt.Errorf("invalid PORT: %q", v)
		}
		cfg.Server.Port = p
	}
	if v := os.Getenv("DINGTALK_WEBHOOK"); v != "" {
		cfg.Push.Dingtalk.Webhook = v
	}
	if v := os.Getenv("DINGTALK_SECRET"); v != "" {
		cfg.Push.Dingtalk.Secret = v
	}
	return nil
}
