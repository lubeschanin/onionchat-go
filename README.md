# onionchat-go

Anonymous chat server for Tor Onion Services. Single binary, zero dependencies, no JavaScript.

Go implementation of [onionchat](https://github.com/lubeschanin/onionchat) (Python).

## Quickstart

```bash
go build -o onionchat .
./onionchat
```

Starts on `127.0.0.1:8181`. No runtime dependencies.

Or run directly:

```bash
go run .
```

## How it works

```
Browser                          Server (127.0.0.1:8181)
  |                                |
  |-- GET / ---------------------->|  Main page (form + chat iframe)
  |-- GET /messages -------------->|  Streaming HTML (open connection)
  |-- GET /clock ----------------->|  UTC clock (auto-refresh 30s)
  |                                |
  |-- POST /send ----------------->|  Append message, notify streams
  |<-- 303 -> / ------------------|  Page reloads (fade-in, autofocus)
  |                                |
  |   <-- new <div> chunks --------|  All streams get the new message
```

Messages are pushed via HTTP streaming (`http.Flusher`). No polling, no refresh, no JavaScript.

## Features

- **Single binary** — `go build`, copy to server, done
- **Zero dependencies** — only Go standard library
- **Zero JavaScript** — pure HTML + CSS + HTTP streaming
- **Anonymous nicknames** — randomly generated (e.g. `Shadow-7a3b`), stored in cookie
- **Real-time delivery** — messages pushed via channel notification, not polling
- **Ephemeral** — in-memory ring buffer (200 messages). Process dies, everything is gone.
- **JSON API** — `GET /api/messages` and `GET /api/status`
- **Hardened** — CSP, rate limiting, body size limit, duplicate filter, HTTP timeouts

## Endpoints

| Method | Path | Description |
|---|---|---|
| `GET` | `/` | Main page |
| `GET` | `/messages` | Streaming message feed (long-lived connection) |
| `GET` | `/clock` | Date and time (YYYY-MM-DD HH:MM UTC) |
| `POST` | `/send` | Send a message (form data: `msg`) |
| `GET` | `/api/messages` | JSON array of all messages (ISO 8601 timestamps) |
| `GET` | `/api/status` | JSON: streams, messages, limits |
| `GET` | `/favicon.ico` | Empty (204) |

## Tor configuration

Add to your `torrc`:

```
HiddenServiceDir /var/lib/tor/onionchat/
HiddenServicePort 80 127.0.0.1:8181
```

Reload Tor, then find your `.onion` address:

```bash
sudo systemctl reload tor
cat /var/lib/tor/onionchat/hostname
```

To clear all messages, restart the process.

## Security

| Measure | Detail |
|---|---|
| XSS | `html.EscapeString()` on all user content, CSP blocks all scripts |
| Body size | `http.MaxBytesReader` rejects >2 KB (413) |
| Rate limiting | 1 msg/s per nickname |
| Duplicate filter | Same text from same nick blocked within 30s |
| Message length | 500 runes max (UTF-8 safe) |
| Stream limit | 100 concurrent connections |
| Cookie | `HttpOnly`, `SameSite=Strict` |
| Headers | CSP, X-Content-Type-Options, Referrer-Policy, X-Frame-Options |
| Server header | `onionchat` (Go default hidden) |
| HTTP timeouts | `ReadHeaderTimeout: 10s`, `IdleTimeout: 120s` (Slowloris mitigation) |
| Stream delivery | Subscribe before snapshot — no missed messages |
| 404 | Empty response, no framework fingerprint |

**Note on CPU usage:** The streaming connection keeps the browser in a "loading" state, which can use 15-20% CPU in Tor Browser. Press `X` (stop loading) to pause the stream and reduce CPU — you will still see all messages loaded so far.

## Limits

| Resource | Limit |
|---|---|
| Messages in memory | 200 (ring buffer) |
| Message length | 500 runes |
| Request body | 2 KB |
| Concurrent streams | 100 |
| Rate limit | 1 msg/s per nick |
| Duplicate window | 30s |

## Tests

```bash
go test -v ./...
```

29 tests covering XSS, rate limiting, duplicate filter, cookie validation, security headers, body limits, stream limits, API endpoints, and more.

## Cross-compile

```bash
GOOS=linux GOARCH=amd64 go build -o onionchat-linux .
GOOS=linux GOARCH=arm64 go build -o onionchat-linux-arm .
```

## Project structure

```
onionchat-go/
├── main.go          # Server (502 lines)
├── main_test.go     # Tests (29 tests)
├── go.mod
└── README.md
```

## See also

- [onionchat](https://github.com/lubeschanin/onionchat) — Python implementation (FastAPI, 345 lines, 35 tests)

## License

MIT
