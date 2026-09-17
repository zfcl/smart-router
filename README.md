# Smart Router

A local, single-binary adaptive provider router for OpenRouter. It defaults to `z-ai/glm-5.3-flash`, but the model can be changed in Settings. It works as an OpenAI-compatible proxy for clients such as Zcode.

## What it does

Instead of choosing `:floor` (cheapest) or `:nitro` (fastest), the router evaluates each OpenRouter endpoint against the actual request:

- estimated input and output token mix;
- prompt/completion price;
- P90 output throughput;
- P90 first-token latency;
- 5m / 30m / 1d uptime;
- tail jitter between P50 and P90;
- local transport success history;
- hard price/context guardrails.

It then injects a dynamic `provider.order` into the request and lets OpenRouter retain automatic fallback.

## Routing algorithm

For endpoint `i`:

```text
C_i = input_tokens * prompt_price_i + output_tokens * completion_price_i
T_i = latency_p90_i + output_tokens / throughput_p90_i
P_i = 0.55 * uptime_5m + 0.30 * uptime_30m + 0.15 * uptime_1d

C*_i = C_i / P_i
T*_i = T_i / P_i

J_i = sqrt((throughput_p50 / throughput_p90) * (latency_p90 / latency_p50))
Risk_i = exp(25 * (1 - P_i))
```

Endpoints that are Pareto-dominated on effective cost, effective time, jitter and risk are penalized. The final scale-invariant utility is computed in log space:

```text
Loss_i = wc * ln(C*_i / median(C*))
       + wt * ln(T*_i / median(T*))
       + ws * ln(J_i / median(J))
       + wr * ln(Risk_i / median(Risk))

Score_i = exp(-Loss_i)
```

Default normalized weights:

- cost: 45%
- completion time: 30%
- reliability risk: 15%
- tail stability: 10%

This gives diminishing returns to raw speed: 10 → 40 tok/s is worth much more than 90 → 120 tok/s, while still allowing long-output jobs to prefer a faster provider when the time saved is large enough.

## Windows use

1. Run `Smart-Router.exe`.
2. The dashboard opens at `http://127.0.0.1:8787`.
3. In Zcode set:

```text
Base URL: http://127.0.0.1:8787/v1
Model:    z-ai/glm-5.3-flash
API Key:  your existing OpenRouter API key
```

The first routed request supplies the API key to the proxy. The key is kept in memory for telemetry refresh and is not written to disk. You can also paste the key into the Integration screen before the first request; it is likewise memory-only.

## Privacy

- binds only to `127.0.0.1`;
- does not log prompt or response content;
- does not persist the OpenRouter API key;
- only route metadata and transport status are shown in logs;
- model inference remains on OpenRouter providers, not locally.

## Configuration

The dashboard can change weights, safety price ceilings, model and telemetry refresh interval. Config is persisted under the OS user config directory (`SmartRouter/config.json`).

Changing the port requires a restart.

## Build from source

Requires Go 1.23+.

```bash
go test ./...
go build -trimpath -ldflags="-s -w" -o Smart-Router .
```

Cross-compile from Linux/macOS for 64-bit Windows:

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -H=windowsgui" -o Smart-Router.exe .
```

## UI direction

The interface follows the `zfcl/visual-style-skills` aesthetic direction: strong hierarchy, neutral surfaces, restrained accent usage, generous negative space, typography-first structure, weak decoration and high information clarity.

## Reliability notes

- `provider.order` is generated from OpenRouter endpoint `tag` values (provider slugs), while the UI keeps the human-readable provider name.
- A key loaded from the dashboard is forwarded to requests that omit `Authorization`; it remains RAM-only.
- Endpoint refreshes are coalesced so simultaneous Agent calls do not create a telemetry-refresh stampede.
- A second launch reopens the dashboard only if the process on the configured port is actually Smart Router; unrelated port conflicts fail explicitly.
- The project is tested with `go test -race ./...` and `go vet ./...`.
