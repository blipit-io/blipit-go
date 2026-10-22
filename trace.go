package blipit

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxBatch  = 1000
	maxBuffer = 10000
	spanEvery = 5 * time.Second
)

type spanKey struct{}

type Span struct {
	c        *client
	traceID  string
	spanID   string
	parentID string
	name     string
	kind     int
	method   string
	start    time.Time
	end      time.Time
	err      bool
	attrs    map[string]any
	done     atomic.Bool
}

type spanBuffer struct {
	c     *client
	mu    sync.Mutex
	spans []*Span
	timer *time.Timer
	busy  atomic.Int64
}

func newSpanBuffer(c *client) *spanBuffer {
	return &spanBuffer{c: c}
}

func StartRequest(ctx context.Context, method, path string) (context.Context, *Span) {
	c := current.Load()
	if c == nil || c.opts.TracesSampleRate <= 0 || rand.Float64() >= c.opts.TracesSampleRate {
		return ctx, nil
	}
	method = strings.Clone(strings.ToUpper(cut(method, 16)))
	path = strings.Clone(cut(strings.SplitN(path, "?", 2)[0], 512))
	if path == "" {
		path = "/"
	}
	s := &Span{c: c, traceID: newID(16), spanID: newID(8), kind: 2, method: method, name: method + " " + path, start: time.Now(), attrs: map[string]any{"http.request.method": method, "url.path": path}}
	return context.WithValue(ctx, spanKey{}, s), s
}

func (s *Span) SetRoute(route string) {
	if s == nil || route == "" {
		return
	}
	route = strings.Clone(cut(strings.SplitN(route, "?", 2)[0], 512))
	if _, pattern, ok := strings.Cut(route, " "); ok && strings.HasPrefix(pattern, "/") {
		route = pattern
	}
	s.name = s.method + " " + route
	s.attrs["http.route"] = route
}

func (s *Span) End(r *http.Request, status func() int) {
	v := recover()
	code := http.StatusOK
	if status != nil {
		code = status()
	}
	if v != nil {
		code = http.StatusInternalServerError
		if v != http.ErrAbortHandler {
			capturePanic(v, requestOf(r))
		}
	}
	if s != nil {
		s.attrs["http.response.status_code"] = code
		s.finish(code >= 500)
	}
	if v != nil {
		panic(v)
	}
}

func Trace(ctx context.Context, name string, fn func(context.Context) error) error {
	parent, _ := ctx.Value(spanKey{}).(*Span)
	if parent == nil {
		return fn(ctx)
	}
	s := &Span{c: parent.c, traceID: parent.traceID, spanID: newID(8), parentID: parent.spanID, name: cut(name, 512), kind: 1, start: time.Now(), attrs: map[string]any{}}
	err := fn(context.WithValue(ctx, spanKey{}, s))
	s.finish(err != nil)
	return err
}

func Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, span := StartRequest(r.Context(), r.Method, r.URL.Path)
		if span != nil {
			r = r.WithContext(ctx)
		}
		sw := &statusWriter{ResponseWriter: w}
		defer span.End(r, func() int {
			span.SetRoute(r.Pattern)
			return sw.code()
		})
		next.ServeHTTP(sw, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) code() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("blipit: the response writer does not support hijacking")
}

func (s *Span) finish(failed bool) {
	if s == nil || !s.done.CompareAndSwap(false, true) {
		return
	}
	s.end = time.Now()
	s.err = s.err || failed
	s.c.spans.add(s)
}

func (b *spanBuffer) add(s *Span) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.spans) >= maxBuffer {
		return
	}
	b.spans = append(b.spans, s)
	if len(b.spans) >= maxBatch {
		go b.flush()
	} else if b.timer == nil {
		b.timer = time.AfterFunc(spanEvery, b.flush)
	}
}

func (b *spanBuffer) flush() {
	b.busy.Add(1)
	defer b.busy.Add(-1)
	for {
		b.mu.Lock()
		if b.timer != nil {
			b.timer.Stop()
			b.timer = nil
		}
		n := min(len(b.spans), maxBatch)
		batch := b.spans[:n:n]
		b.spans = b.spans[n:]
		b.mu.Unlock()
		if n == 0 {
			return
		}
		b.send(batch)
	}
}

func (b *spanBuffer) send(batch []*Span) {
	if wait := time.Until(time.Unix(0, b.c.pauseUntil.Load())); wait > 0 || b.c.refused() {
		return
	}
	body, err := json.Marshal(b.otlp(batch))
	if err != nil {
		return
	}
	res, err := b.c.post("/v1/traces", body)
	if err != nil {
		b.c.debugf("[blipit] spans not sent: %v", err)
		return
	}
	reply, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	res.Body.Close()
	if b.c.mismatch(res.StatusCode, reply) {
		return
	}
	if res.StatusCode == http.StatusTooManyRequests {
		b.c.pause(res, nil)
	} else if res.StatusCode >= 400 {
		b.c.debugf("[blipit] spans rejected %d", res.StatusCode)
	}
}

func attr(k string, v any) map[string]any {
	switch x := v.(type) {
	case int:
		return map[string]any{"key": k, "value": map[string]any{"intValue": x}}
	case float64:
		return map[string]any{"key": k, "value": map[string]any{"doubleValue": x}}
	case bool:
		return map[string]any{"key": k, "value": map[string]any{"boolValue": x}}
	default:
		return map[string]any{"key": k, "value": map[string]any{"stringValue": fmt.Sprint(x)}}
	}
}

func (b *spanBuffer) resource() []map[string]any {
	host, _ := os.Hostname()
	service := b.c.opts.ServerName
	if service == "" {
		service = filepath.Base(os.Args[0])
	}
	pairs := [][2]string{
		{"service.name", service},
		{"host.name", host},
		{"service.instance.id", fmt.Sprintf("%s:%d", host, os.Getpid())},
		{"deployment.environment", b.c.opts.Environment},
		{"service.version", b.c.opts.Release},
		{"process.runtime.version", runtime.Version()},
		{"telemetry.sdk.name", sdkName},
	}
	var out []map[string]any
	for _, p := range pairs {
		if p[1] != "" {
			out = append(out, attr(p[0], p[1]))
		}
	}
	return out
}

func (b *spanBuffer) otlp(batch []*Span) map[string]any {
	spans := make([]map[string]any, 0, len(batch))
	for _, s := range batch {
		attrs := []map[string]any{attr("blipit.sample_rate", b.c.opts.TracesSampleRate)}
		for k, v := range s.attrs {
			attrs = append(attrs, attr(k, v))
		}
		status := 1
		if s.err {
			status = 2
		}
		row := map[string]any{
			"traceId":           s.traceID,
			"spanId":            s.spanID,
			"name":              s.name,
			"kind":              s.kind,
			"startTimeUnixNano": fmt.Sprint(s.start.UnixNano()),
			"endTimeUnixNano":   fmt.Sprint(s.end.UnixNano()),
			"attributes":        attrs,
			"status":            map[string]int{"code": status},
		}
		if s.parentID != "" {
			row["parentSpanId"] = s.parentID
		}
		spans = append(spans, row)
	}
	return map[string]any{"resourceSpans": []any{map[string]any{
		"resource":   map[string]any{"attributes": b.resource()},
		"scopeSpans": []any{map[string]any{"scope": map[string]string{"name": sdkName}, "spans": spans}},
	}}}
}
