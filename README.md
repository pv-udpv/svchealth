# svchealth — Service Health Board

[![CI](https://github.com/pv-udpv/svchealth/actions/workflows/ci.yml/badge.svg)](https://github.com/pv-udpv/svchealth/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/pv-udpv/svchealth.svg)](https://pkg.go.dev/github.com/pv-udpv/svchealth)
[![Go 1.22+](https://img.shields.io/badge/Go-1.22%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/dl/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](./LICENSE)

A terminal Service Health Board built with [Bubble Tea](https://github.com/charmbracelet/bubbletea) and [Lipgloss](https://github.com/charmbracelet/lipgloss). Runs automated health checks against a configured list of API endpoints, discovers specs by URI (OpenAPI / Swagger / JSON Schema), classifies status with color-coded indicators (🟢/🟡/🔴), tracks short-term uptime history, and shows local + remote host metrics — all in a compact, resize-responsive view for quick infrastructure diagnostics.

## Features

- **Automated health checks** — each endpoint is polled on its own cadence (`interval_seconds`), with manual ping on demand.
- **Spec discovery + derivation** — give an endpoint a `spec_uri` instead of a `url`. The tool fetches the OpenAPI/Swagger/JSON-Schema document, resolves the server base URL, and derives a concrete health-check target (health/ready paths are preferred automatically).
- **Three-state classification** with **per-endpoint latency thresholds**
  - 🟢 `UP` — matched expected status (default: any 2xx) under the degraded threshold
  - 🟡 `DEGRADED` — healthy status but latency above `degraded_latency_ms`
  - 🔴 `DOWN` — connection error, timeout, unexpected status, or latency above `critical_latency_ms`

  Thresholds can be set globally in `[settings]` and overridden per endpoint (`degraded_latency_ms`, `critical_latency_ms`); `0` inherits the global value.
- **Short-term uptime history** — every check is persisted to SQLite; the board shows a rolling uptime % and a per-endpoint colored sparkline.
- **System metrics** — a header bar shows local **load average** and **disk usage**; endpoints exposing a JSON `metrics_path` also surface remote `load1` / `disk_used_pct` inline.
- **Responsive layout** — columns, name truncation, and sparkline width adapt live to terminal resizing.
- **TOML config** — add/remove/edit endpoints without touching code.
- **Detail / expand pane** — press `enter` (or `d`) on an endpoint to open a bordered pane showing latency percentiles (p50/p90/p99/min/max), up/degraded/down sample counts over the window, an inline **latency-over-time mini-graph** (`lat/time`), **rolling 1h and 24h aggregates** (uptime %, p50, p99, sample count), discovered spec metadata + derived paths, live edge/tunnel status (when Cloudflare is wired), and the most recent errors with timestamps.
- **Worst-first sort, filter & search** — `s` toggles worst-first ordering (down → degraded → up), `f` filters to problems only, and `/` enters incremental name search.
- **Supabase mirroring (optional)** — when env vars are set, every check is also batched to a Supabase table over PostgREST, in parallel with the local SQLite history. Fully decoupled and nil-safe: with no env vars the tool writes only to SQLite.
- **Real connectors (optional, env-driven)** — concrete implementations of all three hooks, each enabled only when its env vars are present:
  - **Linear** `Notifier` — opens a Linear issue on sustained DOWN (idempotent per outage) and comments on recovery.
  - **Cloudflare** `EdgeStatus` — reports zone status + healthy/total `cloudflared` tunnel counts for an endpoint's host (cached ~30s), surfaced in the detail pane.
  - **Clerk** `AuthProvider` — mints a short-lived Clerk session token and injects it as a `Bearer` header on protected endpoints (cached/refreshed before expiry).
  - **Telegram** `Notifier` — sends a 🔴 DOWN / 🟢 RECOVERED Markdown message to a chat via the Bot API on sustained-down / recovery events. Composes with Linear: when both are configured, events fan out to all notifiers (`MultiNotifier`).

  With none configured, the binary stays fully self-contained — all hooks fall back to no-ops.
- **Debounced alerts (hysteresis)** — notifier hooks fire only after `alert_after` consecutive DOWN checks and clear only after `alert_clear_after` consecutive healthy checks, so flapping endpoints don't spam alerts.
- **HTTP metrics / export endpoint (optional)** — set `metrics_listen` (or `SVCHEALTH_METRICS_LISTEN`) to expose a small HTTP server, decoupled from the TUI and reading only from the local store:
  - `/metrics` — Prometheus text exposition (`svchealth_up`, `svchealth_status`, `svchealth_latency_ms`, `svchealth_http_status`, `svchealth_uptime_ratio`, all labeled by `endpoint`).
  - `/snapshot` — current per-endpoint state as indented JSON.
  - `/healthz` — liveness probe for the exporter itself.

## Build & run

```bash
go build -o bin/svchealth ./cmd/svchealth
./bin/svchealth -config config.toml
```

Requires Go 1.22+ (pure-Go SQLite driver, no cgo; built/tested on Go 1.25). The binary is self-contained.

### Dev harnesses (no TTY needed)

```bash
go run ./cmd/smoketest   # exercises config, discovery, checks, store, metrics
go run ./cmd/viewtest    # renders the TUI View() at two widths
```

## Keybindings

| Key            | Action                          |
| -------------- | ------------------------------- |
| `↑`/`k`, `↓`/`j` | move selection                |
| `enter` / `d`  | toggle detail/expand pane       |
| `r` / `space`  | manual ping selected endpoint   |
| `R`            | manual ping **all** endpoints   |
| `s`            | toggle worst-first sort         |
| `f`            | toggle problems-only filter     |
| `/`            | search by name (`enter`/`esc` to apply) |
| `g` / `G`      | jump to top / bottom            |
| `esc`          | close detail · clear search · clear filter |
| `q` / `ctrl+c` | quit                            |

## Environment variables (all optional)

Every integration below is enabled only when its variables are set; otherwise the feature is silently skipped and the tool runs standalone.

| Variable | Purpose |
| -------- | ------- |
| `SVCHEALTH_SUPABASE_URL` | Supabase project URL (e.g. `https://<ref>.supabase.co`) |
| `SVCHEALTH_SUPABASE_KEY` | Supabase anon/publishable key |
| `SVCHEALTH_SUPABASE_TABLE` | Target table (default `svchealth_samples`) |
| `SVCHEALTH_LINEAR_API_KEY` | Linear personal API key |
| `SVCHEALTH_LINEAR_TEAM_ID` | Linear team UUID to file issues under |
| `SVCHEALTH_CF_API_TOKEN` | Cloudflare API token |
| `SVCHEALTH_CF_ZONE_ID` | Zone whose status to report (optional) |
| `SVCHEALTH_CF_ACCOUNT_ID` | Account to enumerate tunnels under (optional) |
| `SVCHEALTH_CLERK_SECRET_KEY` | Clerk backend API secret key |
| `SVCHEALTH_CLERK_SESSION_ID` | Clerk session id to mint tokens for |
| `SVCHEALTH_CLERK_TEMPLATE` | Optional Clerk JWT template name |
| `SVCHEALTH_CLERK_ENDPOINTS` | Comma list of endpoint names to auth (empty = all) |
| `SVCHEALTH_TELEGRAM_BOT_TOKEN` | Telegram Bot API token (from @BotFather) |
| `SVCHEALTH_TELEGRAM_CHAT_ID` | Target chat id for DOWN/RECOVERED messages |
| `SVCHEALTH_METRICS_LISTEN` | Listen addr for the HTTP exporter (e.g. `127.0.0.1:9899`); overrides `metrics_listen` |

The Supabase table can be created with:

```sql
create table if not exists public.svchealth_samples (
  id          bigserial primary key,
  endpoint    text        not null,
  status      smallint    not null,
  status_text text,
  http_status int,
  latency_ms  int,
  checked_at  timestamptz not null,
  err         text,
  inserted_at timestamptz not null default now()
);
create index if not exists idx_svchealth_samples_ep_time
  on public.svchealth_samples (endpoint, checked_at desc);
```

## Configuration

See [`config.toml`](./config.toml). Key fields:

```toml
[settings]
interval_seconds    = 30      # default polling cadence
timeout_seconds     = 8       # default per-request timeout
history_size        = 60      # samples kept per endpoint (sparkline length)
db_path             = "svchealth.db"
degraded_latency_ms = 1500    # 2xx slower than this -> YELLOW
critical_latency_ms = 5000    # 2xx slower than this -> RED (0 = off)
alert_after         = 3       # consecutive DOWN checks before a notifier fires
alert_clear_after   = 1       # consecutive healthy checks before recovery fires
metrics_listen      = ""      # e.g. "127.0.0.1:9899" to enable /metrics + /snapshot
show_local_metrics  = true

[[endpoint]]
name          = "github-api"
url           = "https://api.github.com"
expect_status = 200           # 0 = any 2xx
interval_seconds = 60         # per-endpoint override
degraded_latency_ms = 800     # per-endpoint threshold (0 = inherit global)
critical_latency_ms = 3000    # per-endpoint threshold (0 = inherit global)

[[endpoint]]                  # spec-derived target
name     = "petstore-spec"
spec_uri = "https://petstore3.swagger.io/api/v3/openapi.json"

[[endpoint]]                  # protected + remote metrics
name         = "internal-svc"
url          = "https://svc.example.ts.net/healthz"
metrics_path = "https://svc.example.ts.net/metrics"
[endpoint.headers]
Authorization = "Bearer <token>"
```

## Architecture

```
cmd/svchealth        entrypoint: load config -> open store -> build engine -> run TUI
internal/config      TOML loader + validation + defaults
internal/specs       OpenAPI/Swagger/JSON-Schema discovery -> derived targets
internal/checks      Checker (single check + classify) + Engine (orchestration, streak/hooks, auth)
internal/store       SQLite history + stats (percentiles) + colored sparkline + Supabase mirror
internal/metrics     local load/disk (procfs + statfs) + remote JSON metrics
internal/connectors  Notifier / EdgeStatus / AuthProvider interfaces + Linear/Cloudflare/Clerk/Telegram impls + MultiNotifier
internal/exporter    HTTP /metrics (Prometheus) + /snapshot (JSON) + /healthz, reading from the store
internal/ui          Bubble Tea model: responsive table, detail pane, latency graph + 1h/24h aggregates, sort/filter/search, color
```

### Connectors

Connectors are assembled from the environment in `cmd/svchealth/main.go` via `buildHooks()`. Each `New*FromEnv()` returns `(nil, nil)` when its variables are absent, leaving the corresponding hook as a no-op:

```go
hooks, _ := buildHooks()           // Linear / Telegram / Cloudflare / Clerk if env is set
eng := checks.NewEngine(ctx, cfg, hooks)
```

- **Linear** — `OnSustainedDown` fires after `alert_after` consecutive RED checks (idempotent per outage); `OnRecovered` posts a recovery comment.
- **Cloudflare** — `TunnelHealthy(host)` queries `GET /zones/{id}` and `GET /accounts/{id}/cfd_tunnel`, caches for ~30s, and is shown in the detail pane.
- **Clerk** — `Authorize(endpoint)` mints a token via `POST /v1/sessions/{id}/tokens` and returns an `Authorization: Bearer` header, scoped by `SVCHEALTH_CLERK_ENDPOINTS` when set.
- **Telegram** — `OnSustainedDown` / `OnRecovered` POST a Markdown message to `https://api.telegram.org/bot<token>/sendMessage`. When more than one notifier is configured, a `MultiNotifier` fans each event to all of them.

Alerts are debounced with hysteresis: a notifier fires only after `alert_after` consecutive DOWN checks and a recovery fires only after `alert_clear_after` consecutive healthy checks.

To add your own integration, implement the interface in `internal/connectors` and assign it in `buildHooks()`.

## License

Released under the [MIT License](./LICENSE).

## Notes

- Spec discovery picks the highest-priority derived target. If that path requires query/path parameters (e.g. Swagger Petstore's `/pet/findByStatus`), the check will report the upstream `4xx` — an honest signal that the derived path needs parameters. Point `url` at a parameter-free health path for clean green status.
- History is pruned to a 24h retention window every 5 minutes to keep the SQLite file small.
