## 0.3.0

- Rebuilt on Blipit's own core (blipit-nuggets): no third-party SDK or dependency is left, only the Go standard library. Events go to `POST /api/v1/events` in Blipit's event format with the `X-Blipit-Key` header, and identify as `blipit-go` with `blipit.Version`.
- Panics: `defer blipit.Recover()` reports a panic with its stack, flushes and panics again. `blipit.Handler(mux)` does the same for net/http with the request (scrubbed URL, allowlisted headers).
- Pulse: with `TracesSampleRate` set, `blipit.Handler` times every sampled request, named by its `ServeMux` pattern. `StartRequest`, `Span.SetRoute` and `Span.End` give Gin, Echo, Fiber and any other router the same in a few lines (see the README), and `blipit.Trace(ctx, name, fn)` adds a step inside a request.
- Sending runs in the background from a queue of at most 100 events. A 429 waits for `Retry-After` and retries; 5xx and network errors are retried twice. Over the workspace allowance, events stop for a minute on the client.
- Stack traces come from the capture site for `CaptureException` and from the panicking frame for panics, oldest call first, with module cache and standard library frames marked as not in the app.
- Breadcrumb categories outside Blipit's list are sent as `custom` with the original category in the data, and data keys that look like passwords, tokens or keys are filtered.
- New options: `ServerName` (sent as the `server` tag and the service name on Pulse) and `BeforeSend`. New error `ErrEndpoint`.
- `CaptureSecurity` uses the same wording and levels as the other Blipit SDKs and drops reports without a known kind or an actor.
- Breaking: `blipit.DSN` is removed, and `User`, `Breadcrumb` and `Level` are Blipit's own types (`User` keeps `ID`, `Email`, `Username`; `Breadcrumb` keeps `Category`, `Message`, `Level`, `Data`, `Timestamp`).
- In development the update notice now reads the `latest` field of the event response.

## 0.2.1

- `blipit.Version` is exported. Events carry the `go:github.com/blipit-io/blipit-go` package and version, so the dashboard can show when this project runs an old Blipit SDK.
- In development (`Environment: "development"` or `Debug: true`) the SDK logs once per process with `log.Print` when a newer version is out: `[blipit] 0.2.2 is available (you have 0.2.1) — <why> — go get github.com/blipit-io/blipit-go@latest`. It reads a header on the responses to events it already sends (no extra request). Nothing is logged in production.

## 0.2.0

- `Init` returns the exported `ErrNoKey` or `ErrNoProject` for an empty (or blank) key or project id and Blipit stays off; it never panics.
