package blipit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type request struct {
	path   string
	key    string
	at     time.Time
	body   []byte
	header http.Header
}

type ingest struct {
	*httptest.Server
	mu       sync.Mutex
	received []request
	respond  func(n int, w http.ResponseWriter)
}

func newIngest(t *testing.T, respond func(n int, w http.ResponseWriter)) *ingest {
	t.Helper()
	in := &ingest{respond: respond}
	in.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		in.mu.Lock()
		in.received = append(in.received, request{r.URL.Path, r.Header.Get("X-Blipit-Key"), time.Now(), body, r.Header})
		n := len(in.received)
		in.mu.Unlock()
		if in.respond != nil {
			in.respond(n, w)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"id":"x"}`))
	}))
	t.Cleanup(in.Close)
	return in
}

func (in *ingest) events(t *testing.T) []map[string]any {
	t.Helper()
	in.mu.Lock()
	defer in.mu.Unlock()
	var out []map[string]any
	for _, r := range in.received {
		if r.path != "/api/v1/events" {
			continue
		}
		var e map[string]any
		if err := json.Unmarshal(r.body, &e); err != nil {
			t.Fatalf("event json: %v\n%s", err, r.body)
		}
		out = append(out, e)
	}
	return out
}

func start(t *testing.T, in *ingest, o Options) {
	t.Helper()
	o.Endpoint = in.URL
	if o.Key == "" {
		o.Key = "blipit_pk_abc"
	}
	o.Project = "7"
	if err := Init(o); err != nil {
		t.Fatal(err)
	}
	scope.Lock()
	scope.user, scope.tags, scope.crumbs = nil, map[string]string{}, nil
	scope.Unlock()
	t.Cleanup(func() { current.Store(nil) })
}

func TestEventsReachBlipit(t *testing.T) {
	in := newIngest(t, nil)
	start(t, in, Options{Environment: "test", Release: "app@1.2.3"})
	SetUser(User{ID: "42", Email: "ana@example.com"})
	SetTag("region code", "ap-southeast-1")
	AddBreadcrumb(&Breadcrumb{Category: "cache", Message: "warmed 120 keys", Data: map[string]any{"password": "hunter2", "n": 120}})
	CaptureMessage("hello from go")
	if !Flush(5 * time.Second) {
		t.Fatal("flush timed out")
	}
	in.mu.Lock()
	r := in.received[0]
	in.mu.Unlock()
	if r.path != "/api/v1/events" || r.key != "blipit_pk_abc" || r.header.Get("Content-Type") != "application/json" {
		t.Fatalf("request = %s %q %q", r.path, r.key, r.header.Get("Content-Type"))
	}
	var e Event
	if err := json.Unmarshal(r.body, &e); err != nil {
		t.Fatal(err)
	}
	if e.Message != "hello from go" || e.Environment != "test" || e.Release != "app@1.2.3" || e.Platform != "go" || e.Level != LevelInfo {
		t.Fatalf("event = %+v", e)
	}
	if e.Sdk != (sdkInfo{"blipit-go", Version}) || len(e.EventID) != 32 {
		t.Fatalf("sdk = %+v id = %q", e.Sdk, e.EventID)
	}
	if e.User == nil || e.User.ID != "42" || e.Tags["region_code"] != "ap-southeast-1" {
		t.Fatalf("user = %+v tags = %v", e.User, e.Tags)
	}
	if len(e.Breadcrumbs) != 1 || e.Breadcrumbs[0].Category != "custom" || e.Breadcrumbs[0].Data["category"] != "cache" || e.Breadcrumbs[0].Data["password"] != filtered {
		t.Fatalf("breadcrumbs = %+v", e.Breadcrumbs)
	}
	for _, banned := range []string{"sentry", "envelope", "dsn", "packages"} {
		if strings.Contains(strings.ToLower(string(r.body)), banned) {
			t.Fatalf("payload mentions %q: %s", banned, r.body)
		}
	}
}

func TestExceptionCarriesStack(t *testing.T) {
	in := newIngest(t, nil)
	start(t, in, Options{})
	CaptureException(fmt.Errorf("loading cart: %w", errors.New("connection refused")))
	Flush(5 * time.Second)
	e := in.events(t)[0]
	ex := e["exception"].(map[string]any)
	if ex["type"] != "*errors.errorString" || ex["value"] != "loading cart: connection refused" || ex["handled"] != true {
		t.Fatalf("exception = %v", ex)
	}
	frames := ex["stacktrace"].(map[string]any)["frames"].([]any)
	last := frames[len(frames)-1].(map[string]any)
	if !strings.HasSuffix(last["function"].(string), "TestExceptionCarriesStack") || last["in_app"] != true {
		t.Fatalf("innermost frame = %v", last)
	}
}

func TestRecoverReportsPanicAndRepanics(t *testing.T) {
	in := newIngest(t, nil)
	start(t, in, Options{})
	func() {
		defer func() {
			if v := recover(); v != "boom" {
				t.Fatalf("re-panic = %v", v)
			}
		}()
		defer Recover()
		explode()
	}()
	ex := in.events(t)[0]["exception"].(map[string]any)
	frames := ex["stacktrace"].(map[string]any)["frames"].([]any)
	last := frames[len(frames)-1].(map[string]any)
	if ex["type"] != "panic" || ex["value"] != "boom" || ex["handled"] != false || !strings.HasSuffix(last["function"].(string), ".explode") {
		t.Fatalf("exception = %v last = %v", ex, last)
	}
}

func explode() {
	panic("boom")
}

func TestMiddlewareReportsPanicAndSpan(t *testing.T) {
	in := newIngest(t, nil)
	start(t, in, Options{TracesSampleRate: 1})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /items/{id}", func(w http.ResponseWriter, r *http.Request) { panic(errors.New("nil cart")) })
	mux.HandleFunc("GET /ok", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	app := httptest.NewServer(Handler(mux))
	defer app.Close()
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)
	req, _ := http.NewRequest("GET", app.URL+"/items/9?token=abc&page=2", nil)
	req.Header.Set("Cookie", "sid=secret")
	req.Header.Set("Authorization", "Bearer abc")
	req.Header.Set("User-Agent", "probe/1")
	if _, err := http.DefaultClient.Do(req); err == nil {
		t.Fatal("a panicking handler should drop the connection")
	}
	if res, _ := http.Get(app.URL + "/ok"); res.StatusCode != http.StatusTeapot {
		t.Fatalf("status = %d", res.StatusCode)
	}
	Flush(5 * time.Second)
	e := in.events(t)[0]
	ex := e["exception"].(map[string]any)
	request := e["request"].(map[string]any)
	if ex["value"] != "nil cart" || ex["handled"] != false || e["level"] != "fatal" {
		t.Fatalf("exception = %v", ex)
	}
	if !strings.HasSuffix(request["url"].(string), "/items/9?token=[redacted]&page=2") || request["method"] != "GET" {
		t.Fatalf("request = %v", request)
	}
	headers, _ := json.Marshal(request["headers"])
	if string(headers) != `{"user-agent":"probe/1"}` {
		t.Fatalf("headers = %s", headers)
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	var traces []string
	for _, r := range in.received {
		if r.path == "/v1/traces" {
			traces = append(traces, string(r.body))
		}
	}
	all := strings.Join(traces, "")
	for _, want := range []string{`"name":"GET /items/{id}"`, `"name":"GET /ok"`, `"intValue":500`, `"intValue":418`, `"stringValue":"blipit-go"`, `"kind":2`} {
		if !strings.Contains(all, want) {
			t.Fatalf("spans missing %s: %s", want, all)
		}
	}
}

func TestTraceAddsChildSpan(t *testing.T) {
	in := newIngest(t, nil)
	start(t, in, Options{TracesSampleRate: 1})
	ctx, span := StartRequest(context.Background(), "post", "/checkout/9?card=4111")
	span.SetRoute("/checkout/{id}")
	Trace(ctx, "charge card", func(context.Context) error { return errors.New("declined") })
	func() {
		defer span.End(nil, func() int { return 201 })
	}()
	Flush(5 * time.Second)
	in.mu.Lock()
	defer in.mu.Unlock()
	var body struct {
		ResourceSpans []struct {
			ScopeSpans []struct {
				Spans []struct {
					TraceID, SpanID, ParentSpanID, Name string
					Kind                                int
					Status                              struct{ Code int }
				}
			}
		}
	}
	var spans []struct {
		TraceID, SpanID, ParentSpanID, Name string
		Kind                                int
		Status                              struct{ Code int }
	}
	for _, r := range in.received {
		if r.path == "/v1/traces" {
			if strings.Contains(string(r.body), "4111") {
				t.Fatalf("query string leaked into spans: %s", r.body)
			}
			json.Unmarshal(r.body, &body)
			for _, rs := range body.ResourceSpans {
				for _, ss := range rs.ScopeSpans {
					spans = append(spans, ss.Spans...)
				}
			}
		}
	}
	if len(spans) != 2 {
		t.Fatalf("spans = %+v", spans)
	}
	child, root := spans[0], spans[1]
	if child.Name != "charge card" || child.Kind != 1 || child.Status.Code != 2 || child.ParentSpanID != root.SpanID || child.TraceID != root.TraceID {
		t.Fatalf("child = %+v root = %+v", child, root)
	}
	if root.Name != "POST /checkout/{id}" || root.Kind != 2 || root.Status.Code != 1 || len(root.TraceID) != 32 || len(root.SpanID) != 16 {
		t.Fatalf("root = %+v", root)
	}
}

func TestNoSpansWithoutSampleRate(t *testing.T) {
	in := newIngest(t, nil)
	start(t, in, Options{})
	if _, span := StartRequest(context.Background(), "GET", "/"); span != nil {
		t.Fatal("tracing is off without TracesSampleRate")
	}
}

func TestRateLimitWaitsForRetryAfter(t *testing.T) {
	in := newIngest(t, func(n int, w http.ResponseWriter) {
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":"slow down","retry_after":1}`))
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	start(t, in, Options{})
	CaptureMessage("one")
	if !Flush(5 * time.Second) {
		t.Fatal("flush timed out")
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	if len(in.received) != 2 || in.received[1].at.Sub(in.received[0].at) < 900*time.Millisecond {
		t.Fatalf("got %d requests, gap %s", len(in.received), in.received[len(in.received)-1].at.Sub(in.received[0].at))
	}
	if string(in.received[0].body) != string(in.received[1].body) {
		t.Fatal("the retry should resend the same event")
	}
}

func TestRetryAfterForms(t *testing.T) {
	if got := retryAfter("", []byte(`{"retry_after":3}`)); got != 3*time.Second {
		t.Fatalf("body = %s", got)
	}
	if got := retryAfter("99999", nil); got != 10*time.Minute {
		t.Fatalf("cap = %s", got)
	}
	if got := retryAfter("", nil); got != 5*time.Second {
		t.Fatalf("default = %s", got)
	}
}

func TestServerErrorsRetryThenDrop(t *testing.T) {
	retryGap = time.Millisecond
	defer func() { retryGap = 500 * time.Millisecond }()
	in := newIngest(t, func(n int, w http.ResponseWriter) { w.WriteHeader(http.StatusBadGateway) })
	start(t, in, Options{})
	CaptureMessage("lost")
	Flush(5 * time.Second)
	if n := len(in.events(t)); n != 3 {
		t.Fatalf("attempts = %d", n)
	}
}

func TestQueueIsBounded(t *testing.T) {
	release := make(chan struct{})
	in := newIngest(t, func(n int, w http.ResponseWriter) {
		<-release
		w.WriteHeader(http.StatusAccepted)
	})
	start(t, in, Options{})
	for i := 0; i < 300; i++ {
		CaptureMessage("burst")
	}
	close(release)
	if !Flush(10 * time.Second) {
		t.Fatal("flush timed out")
	}
	if n := len(in.events(t)); n > maxQueue+1 || n < maxQueue {
		t.Fatalf("delivered %d", n)
	}
}

func TestOverAllowancePausesEvents(t *testing.T) {
	in := newIngest(t, func(n int, w http.ResponseWriter) {
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"accepted":false,"dropped":true,"plan":"free"}`))
	})
	start(t, in, Options{})
	CaptureMessage("one")
	Flush(5 * time.Second)
	CaptureMessage("two")
	Flush(5 * time.Second)
	if n := len(in.events(t)); n != 1 {
		t.Fatalf("sent %d while over the allowance", n)
	}
}

func TestSecurity(t *testing.T) {
	in := newIngest(t, nil)
	var out strings.Builder
	log.SetOutput(&out)
	defer log.SetOutput(os.Stderr)
	publicKeyWarned = sync.Once{}
	start(t, in, Options{})
	CaptureSecurity(Security{Kind: "login_failed", Actor: "ana@example.com"})
	Flush(time.Second)
	if len(in.events(t)) != 0 || !strings.Contains(out.String(), "secret key") {
		t.Fatalf("public key sent a security event, log %q", out.String())
	}
	start(t, in, Options{Key: "blipit_sk_abc"})
	CaptureSecurity(Security{Kind: "unknown", Actor: "x"})
	CaptureSecurity(Security{Kind: "login_failed", Actor: " ana@example.com ", IP: "203.0.113.9", Outcome: "bad_password"})
	Flush(5 * time.Second)
	events := in.events(t)
	if len(events) != 1 {
		t.Fatalf("events = %v", events)
	}
	sec, _ := json.Marshal(events[0]["security"])
	if string(sec) != `{"actor":"ana@example.com","ip":"203.0.113.9","kind":"login_failed","outcome":"bad_password"}` || events[0]["level"] != "warning" || events[0]["message"] != "Failed login for ana@example.com" {
		t.Fatalf("security = %s %v", sec, events[0])
	}
}

func TestInitWithoutKeyReturnsErrorAndStaysOff(t *testing.T) {
	in := newIngest(t, nil)
	start(t, in, Options{})
	if err := Init(Options{Key: "  ", Project: "7"}); err != ErrNoKey {
		t.Fatalf("want ErrNoKey, got %v", err)
	}
	if err := Init(Options{Key: "blipit_pk_abc"}); err != ErrNoProject {
		t.Fatalf("want ErrNoProject, got %v", err)
	}
	if err := Init(Options{Key: "blipit_pk_abc", Project: "7", Endpoint: "in.blipit.io"}); err != ErrEndpoint {
		t.Fatalf("want ErrEndpoint, got %v", err)
	}
	CaptureMessage("ignored while off")
	CaptureException(errors.New("ignored"))
	Recover()
	if !Flush(10*time.Millisecond) || len(in.events(t)) != 0 {
		t.Fatal("sent while off")
	}
}

func TestScrubURL(t *testing.T) {
	got := scrubURL("https://shop.test/a?api_key=1&q=shoes&Session=2&flag")
	if got != "https://shop.test/a?api_key=[redacted]&q=shoes&Session=[redacted]&flag" {
		t.Fatalf("got %q", got)
	}
	if cut("héllo", 2) != "h" {
		t.Fatal("cut splits a rune")
	}
}
