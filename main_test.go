package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func testEndpoint(provider string, inM, outM, tp50, tp90, lat50, lat90, uptime float64) Endpoint {
	return Endpoint{
		ProviderName: provider, Status: 0,
		Pricing: map[string]string{
			"prompt":     strconv.FormatFloat(inM/1e6, 'g', -1, 64),
			"completion": strconv.FormatFloat(outM/1e6, 'g', -1, 64),
		},
		ThroughputLast30m: Percentiles{P50: tp50, P90: tp90},
		LatencyLast30m:    Percentiles{P50: lat50, P90: lat90},
		UptimeLast5m:      uptime, UptimeLast30m: uptime, UptimeLast1d: uptime,
	}
}

func TestBalancedRankingRejectsSlowTinySaving(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxPromptPricePerM = 1
	cfg.MaxCompletionPricePerM = 1
	endpoints := []Endpoint{
		testEndpoint("CheapSlow", 0.075, 0.25, 12, 10, 1.5, 2.5, 99.0),
		testEndpoint("Balanced", 0.09, 0.30, 48, 43, 0.8, 1.2, 99.9),
		testEndpoint("FastExpensive", 0.15, 0.50, 100, 85, 0.6, 0.9, 99.9),
	}
	d := rankEndpoints(endpoints, cfg, map[string]LocalProviderStat{}, 4000, 4000)
	if d.Primary != "Balanced" {
		t.Fatalf("expected Balanced, got %s: %+v", d.Primary, d.Rows)
	}
}

func TestLongOutputCanFavorSpeed(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxPromptPricePerM = 1
	cfg.MaxCompletionPricePerM = 1
	cfg.Weights = Weights{Cost: 0.25, Time: 0.55, Stability: 0.10, Reliability: 0.10}
	cfg = normalizeConfig(cfg)
	endpoints := []Endpoint{
		testEndpoint("Balanced", 0.09, 0.30, 48, 43, 0.8, 1.2, 99.9),
		testEndpoint("Fast", 0.15, 0.50, 110, 95, 0.5, 0.8, 99.95),
	}
	d := rankEndpoints(endpoints, cfg, map[string]LocalProviderStat{}, 2000, 16000)
	if d.Primary != "Fast" {
		t.Fatalf("expected Fast for long output, got %s", d.Primary)
	}
}

func TestSafetyCeiling(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxCompletionPricePerM = 0.35
	endpoints := []Endpoint{
		testEndpoint("Allowed", 0.09, 0.30, 40, 35, 1, 1.2, 99.9),
		testEndpoint("Blocked", 0.15, 0.50, 100, 90, 0.5, 0.7, 99.99),
	}
	d := rankEndpoints(endpoints, cfg, map[string]LocalProviderStat{}, 2000, 4000)
	if d.Primary != "Allowed" {
		t.Fatalf("ceiling ignored: %s", d.Primary)
	}
}

func TestProviderOrderUsesTagSlug(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxPromptPricePerM = 1
	cfg.MaxCompletionPricePerM = 1
	e := testEndpoint("Pretty Provider", 0.09, 0.30, 50, 45, 0.7, 1.0, 99.9)
	e.Tag = "pretty-provider"
	d := rankEndpoints([]Endpoint{e}, cfg, map[string]LocalProviderStat{}, 1000, 1000)
	if len(d.Order) != 1 || d.Order[0] != "pretty-provider" {
		t.Fatalf("expected provider slug in order, got %#v", d.Order)
	}
	if d.Primary != "Pretty Provider" || d.PrimaryKey != "pretty-provider" {
		t.Fatalf("unexpected primary display/key: %q / %q", d.Primary, d.PrimaryKey)
	}
}

func TestSessionKeyIsForwardedWhenClientOmitsAuthorization(t *testing.T) {
	var gotAuth, gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	a := newAppWithTarget(upstream.URL)
	a.sessionKey = "Bearer sk-test"
	req := httptest.NewRequest(http.MethodPost, "http://router/v1/chat/completions", strings.NewReader(`{"model":"other/model","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.handleProxy(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", w.Code, w.Body.String())
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("session key was not forwarded, got %q", gotAuth)
	}
	if gotPath != "/api/v1/chat/completions" {
		t.Fatalf("unexpected upstream path %q", gotPath)
	}
}
