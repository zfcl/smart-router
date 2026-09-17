# Smart Router

A local, single-binary adaptive provider router for OpenRouter. It defaults to `z-ai/glm-5.3-flash`, but the model can be changed in Settings. It works as an OpenAI-compatible proxy for clients such as Zcode and other Agent clients.

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

## Android arm64-v8a

Android v1.1.0 uses a native foreground service and packages the **same Go routing/proxy core** used by the desktop build. It is intended for 64-bit ARM Android devices, including Snapdragon-based tablets such as Xiaomi Pad 4 Plus / `clover`.

Requirements:

- ABI: `arm64-v8a` / AArch64
- Android 8.0+ (`minSdk 26`)
- local API: `http://127.0.0.1:8787/v1` by default
- no Termux, root, Python or Node.js required

The Android app keeps the local router alive through a foreground service, uses the dashboard as its tablet UI, and stores only non-secret router settings. The OpenRouter key stays memory-only exactly like the desktop version.

Android source lives under `android/`. `.github/workflows/android.yml` builds an installable arm64 APK and publishes `android-v1.1.0`.

## Privacy

- binds only to `127.0.0.1`;
- does not log prompt or response content;
- does not persist the OpenRouter API key;
- only route metadata and transport status are shown in logs;
- model inference remains on OpenRouter providers, not locally.

## Configuration

The dashboard can change weights, safety price ceilings, model and telemetry refresh interval. Config is persisted under the OS user config directory (`SmartRouter/config.json`). On Android it is stored inside the app-private files directory.

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

Build the Android Go core manually:

```bash
GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -buildmode=pie -ldflags="-s -w" -o libsmart_router_exec.so .
```

The GitHub Android workflow then packages that PIE executable under the APK's `arm64-v8a` native library directory and builds the Android shell with Gradle.

## UI direction

The interface follows the `zfcl/visual-style-skills` aesthetic direction: strong hierarchy, neutral surfaces, restrained accent usage, generous negative space, typography-first structure, weak decoration and high information clarity.

## Reliability notes

- `provider.order` is generated from OpenRouter endpoint `tag` values (provider slugs), while the UI keeps the human-readable provider name.
- A key loaded from the dashboard is forwarded to requests that omit `Authorization`; it remains RAM-only.
- Endpoint refreshes are coalesced so simultaneous Agent calls do not create a telemetry-refresh stampede.
- A second desktop launch reopens the dashboard only if the process on the configured port is actually Smart Router; unrelated port conflicts fail explicitly.
- Android uses a foreground service + partial wake lock so the local proxy is less likely to be suspended while an Agent is using it.
- The shared Go core is tested before both desktop and Android builds.
