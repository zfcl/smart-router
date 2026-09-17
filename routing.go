package main

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

func parsePricePerToken(m map[string]string, key string) float64 {
	s := strings.TrimSpace(m[key])
	if s == "" {
		return 0
	}
	f, _ := strconv.ParseFloat(s, 64)
	return f
}
func perM(x float64) float64 { return x * 1_000_000 }

func reliability(e Endpoint) float64 {
	vals := []struct{ v, w float64 }{{e.UptimeLast5m, 0.55}, {e.UptimeLast30m, 0.30}, {e.UptimeLast1d, 0.15}}
	total, weight := 0.0, 0.0
	for _, x := range vals {
		if x.v > 0 {
			total += (x.v / 100) * x.w
			weight += x.w
		}
	}
	if weight == 0 {
		return 0.985
	}
	p := total / weight
	if p < 0.70 {
		p = 0.70
	}
	if p > 0.99999 {
		p = 0.99999
	}
	return p
}

func safeThroughput(e Endpoint) (float64, float64) {
	p50, p90 := e.ThroughputLast30m.P50, e.ThroughputLast30m.P90
	if p50 <= 0 {
		p50 = p90
	}
	if p90 <= 0 {
		p90 = p50
	}
	if p50 <= 0 {
		p50 = 10
	}
	if p90 <= 0 {
		p90 = p50 * 0.7
	}
	return p50, p90
}
func safeLatency(e Endpoint) (float64, float64) {
	p50, p90 := e.LatencyLast30m.P50, e.LatencyLast30m.P90
	if p50 <= 0 {
		p50 = p90
	}
	if p90 <= 0 {
		p90 = p50
	}
	if p50 <= 0 {
		p50 = 1.5
	}
	if p90 <= 0 {
		p90 = p50 * 1.6
	}
	return p50, p90
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 1
	}
	y := append([]float64(nil), xs...)
	sort.Float64s(y)
	n := len(y)
	if n%2 == 1 {
		return y[n/2]
	}
	return (y[n/2-1] + y[n/2]) / 2
}

func paretoDominated(rows []ScoreRow, i int) bool {
	a := rows[i]
	if !a.Eligible {
		return false
	}
	for j, b := range rows {
		if i == j || !b.Eligible {
			continue
		}
		noWorse := b.EffectiveCostUSD <= a.EffectiveCostUSD && b.EffectiveTimeSec <= a.EffectiveTimeSec && b.Jitter <= a.Jitter && b.RiskFactor <= a.RiskFactor
		strictly := b.EffectiveCostUSD < a.EffectiveCostUSD || b.EffectiveTimeSec < a.EffectiveTimeSec || b.Jitter < a.Jitter || b.RiskFactor < a.RiskFactor
		if noWorse && strictly {
			return true
		}
	}
	return false
}

func rankEndpoints(endpoints []Endpoint, cfg Config, local map[string]LocalProviderStat, inputTokens, outputTokens float64) RouteDecision {
	if outputTokens < 1 {
		outputTokens = cfg.DefaultOutputTokens
	}
	rows := make([]ScoreRow, 0, len(endpoints))
	for _, e := range endpoints {
		provider := strings.TrimSpace(e.ProviderName)
		if provider == "" {
			provider = strings.TrimSpace(e.Tag)
		}
		if provider == "" {
			provider = e.Name
		}
		providerKey := strings.TrimSpace(e.Tag)
		if providerKey == "" {
			providerKey = strings.ToLower(strings.TrimSpace(e.ProviderName))
		}
		if providerKey == "" {
			providerKey = strings.ToLower(strings.TrimSpace(e.Name))
		}
		pp := parsePricePerToken(e.Pricing, "prompt")
		cp := parsePricePerToken(e.Pricing, "completion")
		cache := parsePricePerToken(e.Pricing, "cache_read")
		if cache == 0 {
			cache = parsePricePerToken(e.Pricing, "input_cache_read")
		}
		tp50, tp90 := safeThroughput(e)
		lat50, lat90 := safeLatency(e)
		p := reliability(e)
		cost := inputTokens*pp + outputTokens*cp
		if cost <= 0 {
			cost = 1e-9
		}
		t := lat90 + outputTokens/math.Max(tp90, 1)
		effectiveCost := cost / p
		effectiveTime := t / p
		jitter := math.Sqrt(math.Max(tp50/tp90, 1) * math.Max(lat90/math.Max(lat50, 0.001), 1))
		risk := math.Exp(25 * math.Max(0, 1-p))
		eligible := e.Status == 0 || e.Status == 200
		reason := ""
		if e.Status != 0 && e.Status != 200 {
			eligible = false
			reason = "endpoint unavailable"
		}
		if cfg.MaxPromptPricePerM > 0 && perM(pp) > cfg.MaxPromptPricePerM {
			eligible = false
			reason = "prompt price over safety ceiling"
		}
		if cfg.MaxCompletionPricePerM > 0 && perM(cp) > cfg.MaxCompletionPricePerM {
			eligible = false
			reason = "completion price over safety ceiling"
		}
		maxPrompt := e.MaxPromptTokens
		if maxPrompt <= 0 {
			maxPrompt = e.ContextLength
		}
		if maxPrompt > 0 && inputTokens > float64(maxPrompt)*0.97 {
			eligible = false
			reason = "estimated prompt exceeds endpoint context"
		}
		rows = append(rows, ScoreRow{Provider: provider, ProviderKey: providerKey, Endpoint: e.Name, Tag: e.Tag, Quantization: e.Quantization, PromptPerM: perM(pp), CompletionPerM: perM(cp), CachePerM: perM(cache), ThroughputP50: tp50, ThroughputP90: tp90, LatencyP50: lat50, LatencyP90: lat90, Reliability: p, CostUSD: cost, TimeSeconds: t, EffectiveCostUSD: effectiveCost, EffectiveTimeSec: effectiveTime, Jitter: jitter, RiskFactor: risk, Eligible: eligible, ExclusionReason: reason, Local: local[providerKey]})
	}
	for i := range rows {
		if rows[i].Eligible {
			rows[i].Pareto = !paretoDominated(rows, i)
		}
	}
	costs, times, jitters, risks := []float64{}, []float64{}, []float64{}, []float64{}
	for _, r := range rows {
		if r.Eligible && r.Pareto {
			costs = append(costs, r.EffectiveCostUSD)
			times = append(times, r.EffectiveTimeSec)
			jitters = append(jitters, r.Jitter)
			risks = append(risks, r.RiskFactor)
		}
	}
	if len(costs) == 0 {
		for _, r := range rows {
			if r.Eligible {
				costs = append(costs, r.EffectiveCostUSD)
				times = append(times, r.EffectiveTimeSec)
				jitters = append(jitters, r.Jitter)
				risks = append(risks, r.RiskFactor)
			}
		}
	}
	mc, mt, mj, mr := median(costs), median(times), median(jitters), median(risks)
	eps := 1e-12
	for i := range rows {
		if !rows[i].Eligible {
			rows[i].Score = 0
			continue
		}
		loss := cfg.Weights.Cost*math.Log(math.Max(rows[i].EffectiveCostUSD, eps)/math.Max(mc, eps)) +
			cfg.Weights.Time*math.Log(math.Max(rows[i].EffectiveTimeSec, eps)/math.Max(mt, eps)) +
			cfg.Weights.Stability*math.Log(math.Max(rows[i].Jitter, eps)/math.Max(mj, eps)) +
			cfg.Weights.Reliability*math.Log(math.Max(rows[i].RiskFactor, eps)/math.Max(mr, eps))
		rows[i].Score = math.Exp(-loss)
		if !rows[i].Pareto {
			rows[i].Score *= 0.82
		}
		ls := rows[i].Local
		n := float64(ls.Successes + ls.Failures)
		if n > 0 {
			posterior := (float64(ls.Successes) + 19) / (n + 20)
			rows[i].Score *= math.Pow(posterior, 0.08)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Score > rows[j].Score })
	seen := map[string]bool{}
	order := []string{}
	primary, primaryKey := "", ""
	for _, r := range rows {
		if r.Eligible && r.Score > 0 && r.ProviderKey != "" && !seen[r.ProviderKey] {
			seen[r.ProviderKey] = true
			order = append(order, r.ProviderKey)
			if primaryKey == "" {
				primary, primaryKey = r.Provider, r.ProviderKey
			}
		}
	}
	return RouteDecision{InputTokens: inputTokens, OutputTokens: outputTokens, Rows: rows, Order: order, Primary: primary, PrimaryKey: primaryKey, At: time.Now()}
}

func estimateInputTokens(v any) float64 {
	var cjk, ascii, other int
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			for _, r := range t {
				if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
					cjk++
				} else if r < 128 {
					ascii++
				} else {
					other++
				}
			}
		case []any:
			for _, z := range t {
				walk(z)
			}
		case map[string]any:
			for k, z := range t {
				if k == "image_url" || k == "audio" || k == "video_url" {
					continue
				}
				walk(z)
			}
		}
	}
	walk(v)
	tok := float64(cjk)*0.85 + float64(ascii)/4.0 + float64(other)/2.2
	if tok < 16 {
		tok = 16
	}
	return tok
}

func estimateOutputTokens(body map[string]any, fallback float64) float64 {
	out := fallback
	for _, k := range []string{"max_completion_tokens", "max_tokens"} {
		if v, ok := body[k]; ok {
			if f, ok := asFloat(v); ok && f > 0 && f < out {
				out = f
			}
		}
	}
	if out < 64 {
		out = 64
	}
	return out
}
func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case json.Number:
		f, e := t.Float64()
		return f, e == nil
	}
	return 0, false
}
