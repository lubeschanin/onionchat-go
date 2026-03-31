# CLAUDE.md

## Project

Anonymous chat server for Tor Onion Services. Go implementation.
Sister project: [onionchat](https://github.com/lubeschanin/onionchat) (Python)

## Stack

- Go 1.21+, standard library only
- Zero external dependencies
- Single binary, cross-compilable

## Architecture

- HTTP streaming via `http.Flusher` + channel notification for real-time delivery
- Persistent channel per stream with `subscribe()`/`unsubscribe()`
- Subscribe-before-snapshot pattern to prevent missed messages
- `time.NewTimer` with `Reset()` (not `time.After`) to avoid timer leaks
- `sync.RWMutex` for concurrent access to store
- `http.Server` with `ReadHeaderTimeout` and `IdleTimeout` (Slowloris mitigation)

## Commands

```bash
go build -o onionchat .           # Build
./onionchat                       # Run
go run .                          # Build and run
go test -v ./...                  # Run 29 tests
go test -race ./...               # With race detector
go vet ./...                      # Static analysis
```

## Cross-compile

```bash
GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o onionchat-linux .
GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o onionchat-linux-arm .
```

## Release

```bash
git tag v0.1.0 && git push --tags    # Triggers GitHub Actions cross-compile
```

## Key decisions

- No `/clear` endpoint — restart to clear. Eliminates secret management.
- `rand.Read` errors panic (crypto failure = compromised system)
- Rune-based truncation (`[]rune`) for UTF-8 safe message length limits
- `encoding/json` for API (not `fmt.Sprintf %q` which produces Go escaping, not JSON)
- Duplicate filter: per-nick, 30s window, cleanup cutoff matches duplicate window
- Cookie: HttpOnly, SameSite=Strict, no Secure (plain HTTP, Tor encrypts)
- All timestamps stored ISO 8601, rendered HH:MM in UI

## Constraints

- No JavaScript — all interactivity via HTML forms, CSS, HTTP streaming
- Bind 127.0.0.1:8181 only — Tor forwards traffic
- Single process, in-memory state — ephemeral by design
- No WriteTimeout on server — would kill streaming connections
