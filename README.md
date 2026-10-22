# blipit-go

Blipit error monitoring for Go. Know your app broke before your users tell you.

Standard library only: the module has no dependencies.

## Install

```sh
go get github.com/blipit-io/blipit-go
```

## Quick start

Call `Init` once at startup, before the server starts.

```go
import "github.com/blipit-io/blipit-go"

err := blipit.Init(blipit.Options{
    Key:         "<public key>",
    Project:     "<project id>",
    Environment: "production",
    Release:     "myapp@1.4.2",
})
```

Both values are on the project's Keys page at app.blipit.io. `Init` never panics: with an empty key or project id it returns `blipit.ErrNoKey` or `blipit.ErrNoProject` (and `blipit.ErrEndpoint` for an `Endpoint` that is not an http(s) URL) and Blipit stays off, so log the error instead of exiting:

```go
if err != nil {
    log.Printf("blipit: %v", err)
}
```

Events are sent in the background from a queue of at most 100. Call `blipit.Flush(2 * time.Second)` before the process exits so queued events are delivered.

## Manual capture

```go
blipit.CaptureException(err)
blipit.CaptureMessage("cache miss rate above 50%")
blipit.SetUser(blipit.User{ID: "42", Email: "ana@example.com"})
blipit.SetTag("region", "ap-southeast-1")
blipit.AddBreadcrumb(&blipit.Breadcrumb{Category: "cache", Message: "warmed 120 keys"})
blipit.CaptureSecurity(blipit.Security{Kind: "login_failed", Actor: "ana@example.com", IP: "203.0.113.9"})
```

Login attempts (`CaptureSecurity`) need the project's secret key (init with it on the server) and the Scale plan; with the public key the SDK logs one warning and sends nothing.

## Panics

`defer blipit.Recover()` at the top of `main` or of any goroutine reports a panic with its stack, flushes, and panics again so the program behaves as before.

```go
go func() {
    defer blipit.Recover()
    work()
}()
```

## net/http

Wrap the handler (or `ServeMux`) you serve. Panics are reported with the request and the request is timed on Pulse, named by its `ServeMux` pattern (`GET /items/{id}`).

```go
mux := http.NewServeMux()
mux.HandleFunc("GET /items/{id}", item)
http.ListenAndServe(":8080", blipit.Handler(mux))
```

## Gin

Register it after `gin.Recovery()` so Gin still answers 500.

```go
r := gin.Default()
r.Use(func(c *gin.Context) {
    ctx, span := blipit.StartRequest(c.Request.Context(), c.Request.Method, c.Request.URL.Path)
    span.SetRoute(c.FullPath())
    c.Request = c.Request.WithContext(ctx)
    defer span.End(c.Request, c.Writer.Status)
    c.Next()
})
```

## Echo

```go
e.Use(middleware.Recover())
e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
    return func(c echo.Context) error {
        req := c.Request()
        ctx, span := blipit.StartRequest(req.Context(), req.Method, req.URL.Path)
        span.SetRoute(c.Path())
        c.SetRequest(req.WithContext(ctx))
        defer span.End(c.Request(), func() int { return c.Response().Status })
        if err := next(c); err != nil {
            c.Error(err)
            if c.Response().Status >= 500 {
                blipit.CaptureException(err)
            }
        }
        return nil
    }
})
```

## Fiber

```go
app.Use(recover.New())
app.Use(func(c *fiber.Ctx) error {
    ctx, span := blipit.StartRequest(c.UserContext(), c.Method(), c.Path())
    c.SetUserContext(ctx)
    defer span.End(nil, func() int {
        span.SetRoute(c.Route().Path)
        return c.Response().StatusCode()
    })
    if err := c.Next(); err != nil {
        if herr := c.App().ErrorHandler(c, err); herr != nil {
            return herr
        }
        if c.Response().StatusCode() >= 500 {
            blipit.CaptureException(err)
        }
    }
    return nil
})
```

`StartRequest`, `SetRoute` and `End` work the same for any other router: `End` must be deferred directly, it reports a panic, records the status and panics again.

## Performance

Set `TracesSampleRate: 0.2` in `Options` and a fifth of the requests show up on Pulse. `blipit.Trace(ctx, "charge card", func(ctx context.Context) error { ... })` adds a timed step inside a request.

## Privacy

Request reports keep only the `User-Agent`, `Referer`, `Content-Type` and `Accept-Language` headers, never cookies, `Authorization` or bodies. Query values whose name looks like a token, key, password, secret, session or auth are replaced with `[redacted]`, and breadcrumb data under such names with `[Filtered]`. Spans carry the path without the query string. `Options.BeforeSend` can change or drop (`return nil`) any event before it is queued.

## Rate limits

A 429 from Blipit pauses sending for the `Retry-After` time and then retries the same event. Server errors and network failures are retried twice with backoff. When the workspace is over its allowance, new events are dropped on the client for a minute.

Docs: https://docs.blipit.io/go

## Updates

In development Blipit logs one line when a newer version is out, with the reason and the command. Update with `go get github.com/blipit-io/blipit-go@latest`. Details: https://docs.blipit.io/updates
