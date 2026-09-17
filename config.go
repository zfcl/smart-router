package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const appVersion = "1.0.1"
const defaultModel = "z-ai/glm-5.3-flash"

type Percentiles struct {
	P50 float64 `json:"p50"`
	P75 float64 `json:"p75"`
	P90 float64 `json:"p90"`
	P99 float64 `json:"p99"`
}

type Endpoint struct {
	Name                string            `json:"name"`
	ProviderName        string            `json:"provider_name"`
	Tag                 string            `json:"tag"`
	Quantization        string            `json:"quantization"`
	Pricing             map[string]string `json:"pricing"`
	SupportedParameters []string          `json:"supported_parameters"`
	ThroughputLast30m   Percentiles       `json:"throughput_last_30m"`
	LatencyLast30m      Percentiles       `json:"latency_last_30m"`
	UptimeLast5m        float64           `json:"uptime_last_5m"`
	UptimeLast30m       float64           `json:"uptime_last_30m"`
	UptimeLast1d        float64           `json:"uptime_last_1d"`
	MaxPromptTokens     int64             `json:"max_prompt_tokens"`
	MaxCompletionTokens int64             `json:"max_completion_tokens"`
	ContextLength       int64             `json:"context_length"`
	Status              int               `json:"status"`
}

type Weights struct {
	Cost        float64 `json:"cost"`
	Time        float64 `json:"time"`
	Stability   float64 `json:"stability"`
	Reliability float64 `json:"reliability"`
}

type Config struct {
	Model                  string  `json:"model"`
	Port                   int     `json:"port"`
	RefreshSeconds         int     `json:"refresh_seconds"`
	MaxPromptPricePerM     float64 `json:"max_prompt_price_per_m"`
	MaxCompletionPricePerM float64 `json:"max_completion_price_per_m"`
	DefaultOutputTokens    float64 `json:"default_output_tokens"`
	Weights                Weights `json:"weights"`
}

type LocalProviderStat struct {
	Successes int64   `json:"successes"`
	Failures  int64   `json:"failures"`
	EWMAE2E   float64 `json:"ewma_e2e_seconds"`
}

type ScoreRow struct {
	Provider         string            `json:"provider"`
	ProviderKey      string            `json:"provider_key"`
	Endpoint         string            `json:"endpoint"`
	Tag              string            `json:"tag"`
	Quantization     string            `json:"quantization"`
	PromptPerM       float64           `json:"prompt_per_m"`
	CompletionPerM   float64           `json:"completion_per_m"`
	CachePerM        float64           `json:"cache_per_m"`
	ThroughputP50    float64           `json:"throughput_p50"`
	ThroughputP90    float64           `json:"throughput_p90"`
	LatencyP50       float64           `json:"latency_p50"`
	LatencyP90       float64           `json:"latency_p90"`
	Reliability      float64           `json:"reliability"`
	CostUSD          float64           `json:"expected_cost_usd"`
	TimeSeconds      float64           `json:"expected_time_seconds"`
	EffectiveCostUSD float64           `json:"effective_cost_usd"`
	EffectiveTimeSec float64           `json:"effective_time_seconds"`
	Jitter           float64           `json:"jitter"`
	RiskFactor       float64           `json:"risk_factor"`
	Score            float64           `json:"score"`
	Pareto           bool              `json:"pareto"`
	Eligible         bool              `json:"eligible"`
	ExclusionReason  string            `json:"exclusion_reason,omitempty"`
	Local            LocalProviderStat `json:"local"`
}

type RouteDecision struct {
	InputTokens  float64    `json:"input_tokens"`
	OutputTokens float64    `json:"output_tokens"`
	Rows         []ScoreRow `json:"rows"`
	Order        []string   `json:"order"`
	Primary      string     `json:"primary"`
	PrimaryKey   string     `json:"primary_key"`
	At           time.Time  `json:"at"`
}

type LogEntry struct {
	At      time.Time `json:"at"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

func defaultConfig() Config {
	return Config{
		Model:                  defaultModel,
		Port:                   8787,
		RefreshSeconds:         30,
		MaxPromptPricePerM:     0.20,
		MaxCompletionPricePerM: 0.60,
		DefaultOutputTokens:    4000,
		Weights:                Weights{Cost: 0.45, Time: 0.30, Stability: 0.10, Reliability: 0.15},
	}
}

func normalizeConfig(c Config) Config {
	if strings.TrimSpace(c.Model) == "" {
		c.Model = defaultModel
	}
	if c.Port < 1024 || c.Port > 65535 {
		c.Port = 8787
	}
	if c.RefreshSeconds < 10 {
		c.RefreshSeconds = 10
	}
	if c.RefreshSeconds > 600 {
		c.RefreshSeconds = 600
	}
	if c.DefaultOutputTokens < 64 {
		c.DefaultOutputTokens = 64
	}
	if c.DefaultOutputTokens > 131072 {
		c.DefaultOutputTokens = 131072
	}
	if c.MaxPromptPricePerM < 0 {
		c.MaxPromptPricePerM = 0
	}
	if c.MaxCompletionPricePerM < 0 {
		c.MaxCompletionPricePerM = 0
	}
	w := c.Weights
	if w.Cost < 0 {
		w.Cost = 0
	}
	if w.Time < 0 {
		w.Time = 0
	}
	if w.Stability < 0 {
		w.Stability = 0
	}
	if w.Reliability < 0 {
		w.Reliability = 0
	}
	sum := w.Cost + w.Time + w.Stability + w.Reliability
	if sum <= 0 {
		w = defaultConfig().Weights
		sum = 1
	}
	w.Cost /= sum
	w.Time /= sum
	w.Stability /= sum
	w.Reliability /= sum
	c.Weights = w
	return c
}

func configPath() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		base = "."
	}
	dir := filepath.Join(base, "SmartRouter")
	_ = os.MkdirAll(dir, 0700)
	return filepath.Join(dir, "config.json")
}

func loadConfig() Config {
	c := defaultConfig()
	b, err := os.ReadFile(configPath())
	if err == nil {
		_ = json.Unmarshal(b, &c)
	}
	return normalizeConfig(c)
}

func saveConfig(c Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(), b, 0600)
}
