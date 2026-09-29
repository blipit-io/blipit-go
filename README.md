# blipit-go

Blipit error monitoring for Go. Know your app broke before your users tell you.

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

Both values are on the project's Keys page at app.blipit.io. Call `blipit.Flush(2 * time.Second)` before the process exits so queued events are delivered.

## Manual capture

```go
blipit.CaptureException(err)
blipit.CaptureMessage("cache miss rate above 50%")
blipit.SetUser(blipit.User{ID: "42", Email: "ana@example.com"})
blipit.SetTag("region", "ap-southeast-1")
blipit.AddBreadcrumb(&blipit.Breadcrumb{Category: "cache", Message: "warmed 120 keys"})
blipit.CaptureSecurity(blipit.Security{Kind: "login_failed", Actor: "ana@example.com", IP: "203.0.113.9"})
```

## Performance

Set `TracesSampleRate: 0.2` in `Options` and requests show up on the Performance page.

Docs: https://docs.blipit.io/go
