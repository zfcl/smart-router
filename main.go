package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

//go:embed web/index.html
var webFS embed.FS

func jsonWrite(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}

func (a *App) dashboardHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	b, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "dashboard unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

func (a *App) apiHandler(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/state":
		jsonWrite(w, 200, a.state())
	case "/api/config":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		var c Config
		if err := readJSON(r, &c); err != nil {
			jsonWrite(w, 400, map[string]any{"error": err.Error()})
			return
		}
		c = normalizeConfig(c)
		a.mu.Lock()
		oldPort := a.config.Port
		a.config = c
		a.mu.Unlock()
		if err := saveConfig(c); err != nil {
			a.addLog("warn", "Config save failed: "+err.Error())
		}
		msg := "Settings saved"
		if oldPort != c.Port {
			msg = "Settings saved. Restart required for port change."
		}
		a.addLog("info", msg)
		jsonWrite(w, 200, map[string]any{"ok": true, "message": msg, "config": c})
	case "/api/session-key":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		var x struct {
			Key string `json:"key"`
		}
		if err := readJSON(r, &x); err != nil {
			jsonWrite(w, 400, map[string]any{"error": err.Error()})
			return
		}
		k := strings.TrimSpace(x.Key)
		if k != "" && !strings.HasPrefix(strings.ToLower(k), "bearer ") {
			k = "Bearer " + k
		}
		a.mu.Lock()
		a.sessionKey = k
		a.mu.Unlock()
		a.addLog("info", "OpenRouter key loaded into memory only")
		jsonWrite(w, 200, map[string]any{"ok": true})
	case "/api/refresh":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		a.mu.RLock()
		key := a.sessionKey
		a.mu.RUnlock()
		if key == "" {
			key = authHeader(r)
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		a.refreshMu.Lock()
		err := a.fetchEndpoints(ctx, key)
		a.refreshMu.Unlock()
		if err != nil {
			jsonWrite(w, 502, map[string]any{"error": err.Error()})
			return
		}
		a.mu.RLock()
		cfg := a.config
		eps := append([]Endpoint(nil), a.endpoints...)
		local := make(map[string]LocalProviderStat)
		for k, v := range a.local {
			local[k] = v
		}
		ewma := a.ewmaOutput
		a.mu.RUnlock()
		d := rankEndpoints(eps, cfg, local, 4000, ewma)
		a.mu.Lock()
		a.decision = d
		a.mu.Unlock()
		jsonWrite(w, 200, map[string]any{"ok": true, "decision": d})
	case "/api/stop":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		jsonWrite(w, 200, map[string]any{"ok": true})
		go func() {
			time.Sleep(200 * time.Millisecond)
			if a.server != nil {
				_ = a.server.Shutdown(context.Background())
			}
		}()
	default:
		http.NotFound(w, r)
	}
}

func (a *App) backgroundRefresh(ctx context.Context) {
	for {
		a.mu.RLock()
		d := time.Duration(a.config.RefreshSeconds) * time.Second
		a.mu.RUnlock()
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		a.mu.RLock()
		key := a.sessionKey
		a.mu.RUnlock()
		if key == "" {
			k := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
			if k != "" {
				if !strings.HasPrefix(strings.ToLower(k), "bearer ") {
					k = "Bearer " + k
				}
				key = k
			}
		}
		if key == "" {
			continue
		}
		a.ensureEndpoints(key)
	}
}

func existingRouterRunning(addr string) bool {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get("http://" + addr + "/api/state")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var state struct {
		Version string `json:"version"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&state) != nil {
		return false
	}
	return strings.TrimSpace(state.Version) != ""
}

func openBrowser(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	case "darwin":
		cmd = exec.Command("open", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	_ = cmd.Start()
}

func main() {
	noBrowser := false
	for _, arg := range os.Args[1:] {
		if arg == "--no-browser" {
			noBrowser = true
		}
	}
	a := newApp()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", a.apiHandler)
	mux.HandleFunc("/v1/", a.handleProxy)
	mux.HandleFunc("/", a.dashboardHandler)
	addr := fmt.Sprintf("127.0.0.1:%d", a.config.Port)
	srv := &http.Server{Addr: addr, Handler: securityHeaders(mux, a.config.Port), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	a.server = srv
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.backgroundRefresh(ctx)
	a.addLog("info", fmt.Sprintf("Router started on %s", addr))
	fmt.Printf("Smart Router %s\nDashboard: http://%s\nZcode Base URL: http://%s/v1\nClose this window or use Dashboard > Stop to exit.\n\n", appVersion, addr, addr)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if existingRouterRunning(addr) {
			if !noBrowser {
				openBrowser("http://" + addr)
			}
			fmt.Printf("Router is already running at http://%s\n", addr)
			return
		}
		log.Fatalf("Cannot listen on %s: %v (the port is occupied by another process)", addr, err)
	}
	if !noBrowser {
		go func() { time.Sleep(450 * time.Millisecond); openBrowser("http://" + addr) }()
	}
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func securityHeaders(next http.Handler, port int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowedHostA := fmt.Sprintf("127.0.0.1:%d", port)
		allowedHostB := fmt.Sprintf("localhost:%d", port)
		if r.Host != allowedHostA && r.Host != allowedHostB {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin != "" && origin != "http://"+allowedHostA && origin != "http://"+allowedHostB {
			http.Error(w, "cross-site request blocked", http.StatusForbidden)
			return
		}
		if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
			http.Error(w, "cross-site request blocked", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; font-src 'self'")
		next.ServeHTTP(w, r)
	})
}
