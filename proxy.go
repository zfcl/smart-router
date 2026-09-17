package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type App struct {
	mu             sync.RWMutex
	config         Config
	endpoints      []Endpoint
	endpointsAt    time.Time
	decision       RouteDecision
	sessionKey     string
	logs           []LogEntry
	local          map[string]LocalProviderStat
	requestCount   int64
	forwardedCount int64
	ewmaOutput     float64
	reverse        *httputil.ReverseProxy
	server         *http.Server
	refreshMu      sync.Mutex
}

func newApp() *App {
	return newAppWithTarget("https://openrouter.ai")
}

func newAppWithTarget(rawTarget string) *App {
	target, _ := url.Parse(rawTarget)
	a := &App{config: loadConfig(), local: map[string]LocalProviderStat{}}
	a.ewmaOutput = a.config.DefaultOutputTokens
	rp := &httputil.ReverseProxy{}
	rp.Rewrite = func(pr *httputil.ProxyRequest) {
		pr.SetURL(target)
		pr.Out.URL.Path = "/api" + pr.In.URL.Path
		pr.Out.Host = "openrouter.ai"
		pr.Out.Header.Set("X-Title", "Smart Router")
		pr.Out.Header.Set("HTTP-Referer", "http://127.0.0.1")
	}
	rp.FlushInterval = -1
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		a.addLog("error", "Upstream error: "+err.Error())
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}
	rp.ModifyResponse = func(resp *http.Response) error {
		provider := strings.TrimSpace(resp.Header.Get("x-openrouter-provider"))
		if provider == "" {
			provider = strings.TrimSpace(resp.Header.Get("X-OpenRouter-Provider"))
		}
		if provider == "" {
			a.mu.RLock()
			provider = a.decision.PrimaryKey
			a.mu.RUnlock()
		}
		provider = a.canonicalProviderKey(provider)
		startAny := resp.Request.Context().Value(ctxStartKey{})
		started, _ := startAny.(time.Time)
		original := resp.Body
		resp.Body = &observedBody{ReadCloser: original, onDone: func(n int64, complete bool) {
			elapsed := time.Since(started).Seconds()
			a.observe(provider, resp.StatusCode, elapsed, n, complete)
		}}
		return nil
	}
	a.reverse = rp
	return a
}

type ctxStartKey struct{}

type observedBody struct {
	io.ReadCloser
	once     sync.Once
	n        int64
	complete bool
	onDone   func(int64, bool)
}

func (o *observedBody) finish() {
	o.once.Do(func() {
		if o.onDone != nil {
			o.onDone(o.n, o.complete)
		}
	})
}

func (o *observedBody) Read(p []byte) (int, error) {
	n, err := o.ReadCloser.Read(p)
	o.n += int64(n)
	if errors.Is(err, io.EOF) {
		o.complete = true
		o.finish()
	} else if err != nil {
		o.finish()
	}
	return n, err
}

func (o *observedBody) Close() error {
	o.finish()
	return o.ReadCloser.Close()
}

func (a *App) canonicalProviderKey(provider string) string {
	p := strings.TrimSpace(provider)
	if p == "" {
		return ""
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, e := range a.endpoints {
		if strings.EqualFold(p, e.Tag) || strings.EqualFold(p, e.ProviderName) {
			if strings.TrimSpace(e.Tag) != "" {
				return strings.TrimSpace(e.Tag)
			}
		}
	}
	return strings.ToLower(p)
}

func (a *App) observe(provider string, status int, elapsed float64, bytesOut int64, complete bool) {
	if provider == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.local[provider]
	if status >= 200 && status < 400 && complete {
		s.Successes++
	} else if status == 429 || status >= 500 {
		s.Failures++
	}
	if elapsed > 0 && complete {
		if s.EWMAE2E == 0 {
			s.EWMAE2E = elapsed
		} else {
			s.EWMAE2E = 0.85*s.EWMAE2E + 0.15*elapsed
		}
	}
	a.local[provider] = s
	if complete && bytesOut > 100 {
		est := float64(bytesOut) / 5.0
		if est > 16 && est < 131072 {
			a.ewmaOutput = 0.92*a.ewmaOutput + 0.08*est
		}
	}
}

func (a *App) addLog(level, msg string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.logs = append(a.logs, LogEntry{At: time.Now(), Level: level, Message: msg})
	if len(a.logs) > 120 {
		a.logs = append([]LogEntry(nil), a.logs[len(a.logs)-120:]...)
	}
}

func (a *App) fetchEndpoints(ctx context.Context, key string) error {
	a.mu.RLock()
	model := a.config.Model
	a.mu.RUnlock()
	model = strings.Split(model, ":")[0]
	parts := strings.SplitN(model, "/", 2)
	if len(parts) != 2 {
		return errors.New("model must be author/slug")
	}
	u := fmt.Sprintf("https://openrouter.ai/api/v1/models/%s/%s/endpoints", url.PathEscape(parts[0]), url.PathEscape(parts[1]))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if key != "" {
		req.Header.Set("Authorization", key)
	}
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("endpoints API %s: %s", resp.Status, string(b))
	}
	var payload struct {
		Data struct {
			Endpoints []Endpoint `json:"endpoints"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return err
	}
	if len(payload.Data.Endpoints) == 0 {
		return errors.New("OpenRouter returned no endpoints")
	}
	a.mu.Lock()
	a.endpoints = payload.Data.Endpoints
	a.endpointsAt = time.Now()
	a.mu.Unlock()
	a.addLog("info", fmt.Sprintf("Refreshed %d provider endpoints", len(payload.Data.Endpoints)))
	return nil
}

func (a *App) ensureEndpoints(key string) {
	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()
	a.mu.RLock()
	stale := len(a.endpoints) == 0 || time.Since(a.endpointsAt) > time.Duration(a.config.RefreshSeconds)*time.Second
	a.mu.RUnlock()
	if !stale {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3500*time.Millisecond)
	defer cancel()
	if err := a.fetchEndpoints(ctx, key); err != nil {
		a.addLog("warn", "Provider refresh failed; request will fall back to available cached/default routing: "+err.Error())
	}
}

func authHeader(r *http.Request) string {
	s := strings.TrimSpace(r.Header.Get("Authorization"))
	if s != "" {
		return s
	}
	if k := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")); k != "" {
		if strings.HasPrefix(strings.ToLower(k), "bearer ") {
			return k
		}
		return "Bearer " + k
	}
	return ""
}

func mergePriceCeiling(provider map[string]any, cfg Config) {
	cur, _ := provider["max_price"].(map[string]any)
	if cur == nil {
		cur = map[string]any{}
	}
	setMin := func(k string, v float64) {
		if v <= 0 {
			return
		}
		if old, ok := asFloat(cur[k]); !ok || old <= 0 || v < old {
			cur[k] = v
		}
	}
	setMin("prompt", cfg.MaxPromptPricePerM)
	setMin("completion", cfg.MaxCompletionPricePerM)
	if len(cur) > 0 {
		provider["max_price"] = cur
	}
}

func (a *App) handleProxy(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt64(&a.requestCount, 1)
	key := authHeader(r)
	if key == "" {
		a.mu.RLock()
		key = a.sessionKey
		a.mu.RUnlock()
	}
	if key != "" {
		a.mu.Lock()
		a.sessionKey = key
		a.mu.Unlock()
		if r.Header.Get("Authorization") == "" {
			r.Header.Set("Authorization", key)
		}
	}
	if r.Method == http.MethodPost && (strings.HasSuffix(r.URL.Path, "/chat/completions") || strings.HasSuffix(r.URL.Path, "/responses") || strings.HasSuffix(r.URL.Path, "/completions")) {
		raw, err := io.ReadAll(r.Body)
		if err == nil && len(raw) > 0 {
			_ = r.Body.Close()
			var body map[string]any
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.UseNumber()
			if dec.Decode(&body) == nil {
				model, _ := body["model"].(string)
				a.mu.RLock()
				cfg := a.config
				ewma := a.ewmaOutput
				a.mu.RUnlock()
				if model == "" {
					model = cfg.Model
				}
				if strings.Split(model, ":")[0] == strings.Split(cfg.Model, ":")[0] {
					a.ensureEndpoints(key)
					inTok := estimateInputTokens(body)
					outTok := estimateOutputTokens(body, ewma)
					a.mu.RLock()
					eps := append([]Endpoint(nil), a.endpoints...)
					local := make(map[string]LocalProviderStat, len(a.local))
					for k, v := range a.local {
						local[k] = v
					}
					a.mu.RUnlock()
					decision := rankEndpoints(eps, cfg, local, inTok, outTok)
					a.mu.Lock()
					a.decision = decision
					a.mu.Unlock()
					p, _ := body["provider"].(map[string]any)
					if p == nil {
						p = map[string]any{}
					}
					if len(decision.Order) > 0 {
						p["order"] = decision.Order
						delete(p, "sort")
					}
					if _, ok := p["allow_fallbacks"]; !ok {
						p["allow_fallbacks"] = true
					}
					if _, ok := p["require_parameters"]; !ok {
						p["require_parameters"] = true
					}
					mergePriceCeiling(p, cfg)
					body["provider"] = p
					if len(decision.Order) > 0 {
						a.addLog("route", fmt.Sprintf("%s → %s · score %.3f · est %.0f in / %.0f out", model, decision.Primary, decision.Rows[0].Score, inTok, outTok))
					} else {
						a.addLog("warn", fmt.Sprintf("%s · no eligible ranked provider; hard price guardrails remain active", model))
					}
				}
				if b, err := json.Marshal(body); err == nil {
					raw = b
				}
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			r.ContentLength = int64(len(raw))
			r.Header.Set("Content-Length", strconv.Itoa(len(raw)))
		}
	}
	atomic.AddInt64(&a.forwardedCount, 1)
	ctx := context.WithValue(r.Context(), ctxStartKey{}, time.Now())
	r = r.WithContext(ctx)
	a.reverse.ServeHTTP(w, r)
}

func (a *App) state() map[string]any {
	a.mu.RLock()
	defer a.mu.RUnlock()
	logs := append([]LogEntry(nil), a.logs...)
	if len(logs) > 60 {
		logs = logs[len(logs)-60:]
	}
	return map[string]any{
		"version": appVersion, "config": a.config, "key_ready": a.sessionKey != "" || os.Getenv("OPENROUTER_API_KEY") != "",
		"endpoints_at": a.endpointsAt, "endpoint_count": len(a.endpoints), "decision": a.decision, "logs": logs,
		"request_count": atomic.LoadInt64(&a.requestCount), "forwarded_count": atomic.LoadInt64(&a.forwardedCount),
		"ewma_output_tokens": a.ewmaOutput, "base_url": fmt.Sprintf("http://127.0.0.1:%d/v1", a.config.Port),
		"algorithm": "Pareto filter + scale-invariant weighted geometric utility using effective cost, completion time, jitter and reliability risk",
	}
}
