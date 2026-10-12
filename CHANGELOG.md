## 0.2.1

- `blipit.Version` is exported. Events carry the `go:github.com/blipit-io/blipit-go` package and version, so the dashboard can show when this project runs an old Blipit SDK.
- In development (`Environment: "development"` or `Debug: true`) the SDK logs once per process with `log.Print` when a newer version is out: `[blipit] 0.2.2 is available (you have 0.2.1) — <why> — go get github.com/blipit-io/blipit-go@latest`. It reads a header on the responses to events it already sends (no extra request). Nothing is logged in production.

## 0.2.0

- `Init` returns the exported `ErrNoKey` or `ErrNoProject` for an empty (or blank) key or project id and Blipit stays off; it never panics.
