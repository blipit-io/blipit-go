package blipit

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxQueue  = 100
	maxCrumbs = 50
	maxFrames = 128
	filtered  = "[Filtered]"
)

var (
	current  atomic.Pointer[client]
	noticed  atomic.Bool
	retryGap = 500 * time.Millisecond
	quietFor = time.Minute
)

var scope = struct {
	sync.Mutex
	user   *User
	tags   map[string]string
	crumbs []crumb
}{tags: map[string]string{}}

var (
	sensitiveKey   = regexp.MustCompile(`(?i)pass(word|wd)?|pwd|secret|token|api[_-]?key|auth|cookie|session|credit|card|cvv|ssn`)
	sensitiveParam = regexp.MustCompile(`(?i)token|key|password|secret|auth|session|passwd|api`)
	badTagChar     = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)
	crumbKinds     = map[string]bool{"ui.click": true, "ui.input": true, "navigation": true, "http": true, "console": true, "query": true, "custom": true}
	keptHeaders    = []string{"User-Agent", "Referer", "Content-Type", "Accept-Language"}
)

type Event struct {
	EventID     string            `json:"event_id"`
	Timestamp   string            `json:"timestamp"`
	Platform    string            `json:"platform"`
	Level       Level             `json:"level,omitempty"`
	Message     string            `json:"message,omitempty"`
	Exception   *Exception        `json:"exception,omitempty"`
	Security    *Security         `json:"security,omitempty"`
	Release     string            `json:"release,omitempty"`
	Environment string            `json:"environment,omitempty"`
	User        *User             `json:"user,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
	Breadcrumbs []crumb           `json:"breadcrumbs,omitempty"`
	Request     *Request          `json:"request,omitempty"`
	Contexts    map[string]any    `json:"contexts,omitempty"`
	Sdk         sdkInfo           `json:"sdk"`
}

type Exception struct {
	Type       string      `json:"type"`
	Value      string      `json:"value"`
	Handled    bool        `json:"handled"`
	Stacktrace *Stacktrace `json:"stacktrace,omitempty"`
}

type Stacktrace struct {
	Frames []Frame `json:"frames"`
}

type Frame struct {
	Filename string `json:"filename"`
	Function string `json:"function,omitempty"`
	Lineno   int    `json:"lineno,omitempty"`
	InApp    bool   `json:"in_app"`
}

type Request struct {
	URL     string            `json:"url,omitempty"`
	Method  string            `json:"method,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type sdkInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type crumb struct {
	Timestamp string         `json:"timestamp"`
	Category  string         `json:"category"`
	Message   string         `json:"message,omitempty"`
	Level     Level          `json:"level,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
}

type client struct {
	opts       Options
	http       *http.Client
	queue      chan *Event
	pending    atomic.Int64
	pauseUntil atomic.Int64
	quietUntil atomic.Int64
	spans      *spanBuffer
}

func newClient(o Options) *client {
	c := &client{opts: o, http: &http.Client{Timeout: 10 * time.Second}, queue: make(chan *Event, maxQueue)}
	c.spans = newSpanBuffer(c)
	go c.run()
	return c
}

func (c *client) debugf(format string, args ...any) {
	if c.opts.Debug {
		log.Printf(format, args...)
	}
}

func (c *client) development() bool {
	return c.opts.Debug || c.opts.Environment == "development"
}

func (c *client) send(e *Event) {
	if time.Now().UnixNano() < c.quietUntil.Load() {
		return
	}
	e.EventID = newID(16)
	e.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	e.Platform = "go"
	e.Release = cut(c.opts.Release, 256)
	e.Environment = cut(c.opts.Environment, 64)
	e.Sdk = sdkInfo{sdkName, Version}
	e.Contexts = map[string]any{"runtime": map[string]string{"name": "go", "version": runtime.Version()}, "os": map[string]string{"name": runtime.GOOS}}
	scope.Lock()
	if scope.user != nil {
		u := User{cut(scope.user.ID, 128), cut(scope.user.Email, 256), cut(scope.user.Username, 128)}
		e.User = &u
	}
	e.Tags = map[string]string{}
	if c.opts.ServerName != "" {
		e.Tags["server"] = cut(c.opts.ServerName, 256)
	}
	for k, v := range scope.tags {
		if len(e.Tags) < 32 {
			e.Tags[k] = v
		}
	}
	e.Breadcrumbs = append([]crumb(nil), scope.crumbs...)
	scope.Unlock()
	if c.opts.BeforeSend != nil {
		if e = c.opts.BeforeSend(e); e == nil {
			return
		}
	}
	c.pending.Add(1)
	select {
	case c.queue <- e:
	default:
		c.pending.Add(-1)
		c.debugf("[blipit] queue full, event %s dropped", e.EventID)
	}
}

func (c *client) run() {
	for e := range c.queue {
		c.deliver(e)
		c.pending.Add(-1)
	}
}

func (c *client) deliver(e *Event) {
	body, err := json.Marshal(e)
	if err != nil {
		c.debugf("[blipit] event not encoded: %v", err)
		return
	}
	for attempt := 0; attempt < 3; attempt++ {
		if wait := time.Until(time.Unix(0, c.pauseUntil.Load())); wait > 0 {
			time.Sleep(wait)
		}
		res, err := c.post("/api/v1/events", body)
		if err != nil {
			time.Sleep(retryGap << attempt)
			continue
		}
		reply, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		res.Body.Close()
		switch {
		case res.StatusCode == http.StatusTooManyRequests:
			c.pause(res, reply)
		case res.StatusCode >= 500:
			time.Sleep(retryGap << attempt)
		case res.StatusCode >= 400:
			c.debugf("[blipit] event rejected %d %s", res.StatusCode, reply)
			return
		default:
			c.accepted(reply)
			return
		}
	}
	c.debugf("[blipit] dropping event after retries %s", e.EventID)
}

func (c *client) post(path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, c.opts.Endpoint+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Blipit-Key", c.opts.Key)
	req.Header.Set("User-Agent", sdkName+"/"+Version)
	return c.http.Do(req)
}

func (c *client) pause(res *http.Response, reply []byte) {
	wait := retryAfter(res.Header.Get("Retry-After"), reply)
	c.pauseUntil.Store(time.Now().Add(wait).UnixNano())
	c.debugf("[blipit] rate limited, sending again in %s", wait)
}

func retryAfter(header string, reply []byte) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && s >= 0 {
		return min(time.Duration(s)*time.Second, 10*time.Minute)
	}
	if t, err := http.ParseTime(header); err == nil {
		return min(max(time.Until(t), 0), 10*time.Minute)
	}
	var body struct {
		RetryAfter float64 `json:"retry_after"`
	}
	if json.Unmarshal(reply, &body) == nil && body.RetryAfter > 0 {
		return min(time.Duration(body.RetryAfter*float64(time.Second)), 10*time.Minute)
	}
	return 5 * time.Second
}

func (c *client) accepted(reply []byte) {
	var body struct {
		Dropped bool            `json:"dropped"`
		Reason  string          `json:"reason"`
		Latest  json.RawMessage `json:"latest"`
	}
	if json.Unmarshal(reply, &body) != nil {
		return
	}
	if body.Dropped || body.Reason == "free_plan_suspended" {
		c.quietUntil.Store(time.Now().Add(quietFor).UnixNano())
		c.debugf("[blipit] the workspace is over its allowance, events are paused for %s", quietFor)
	}
	if c.development() && !noticed.Load() {
		if line := updateNotice(body.Latest, Version); line != "" && noticed.CompareAndSwap(false, true) {
			log.Print(line)
		}
	}
}

func updateNotice(raw json.RawMessage, current string) string {
	var latest struct {
		Version any `json:"version"`
		Note    any `json:"note"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &latest) != nil {
		return ""
	}
	version, ok := latest.Version.(string)
	if !ok || version == "" || version == current {
		return ""
	}
	why := ""
	if note, ok := latest.Note.(string); ok && strings.TrimSpace(note) != "" {
		why = " — " + strings.TrimSpace(note)
	}
	return fmt.Sprintf("[blipit] %s is available (you have %s)%s — %s", version, current, why, updateCommand)
}

func (c *client) flush(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	c.spans.flush()
	for c.pending.Load() > 0 || c.spans.busy.Load() > 0 {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
	return true
}

func capturePanic(v any, req *Request) {
	c := current.Load()
	if c == nil {
		return
	}
	var ex *Exception
	if err, ok := v.(error); ok {
		ex = exceptionOf(err, 1, false)
	} else {
		ex = &Exception{Type: "panic", Value: cut(fmt.Sprint(v), 4096), Stacktrace: stackOf(2)}
	}
	c.send(&Event{Level: LevelFatal, Exception: ex, Request: req})
}

func exceptionOf(err error, skip int, handled bool) *Exception {
	inner := err
	for next := errors.Unwrap(inner); next != nil; next = errors.Unwrap(inner) {
		inner = next
	}
	return &Exception{Type: cut(reflect.TypeOf(inner).String(), 256), Value: cut(err.Error(), 4096), Handled: handled, Stacktrace: stackOf(skip + 2)}
}

func stackOf(skip int) *Stacktrace {
	pcs := make([]uintptr, 256)
	n := runtime.Callers(skip+1, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	var out []Frame
	for {
		f, more := frames.Next()
		if f.Function == "runtime.gopanic" {
			out = out[:0]
		} else if f.File != "" {
			out = append(out, Frame{Filename: cut(f.File, 1024), Function: cut(f.Function, 512), Lineno: f.Line, InApp: inApp(f)})
		}
		if !more {
			break
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if len(out) > maxFrames {
		out = out[len(out)-maxFrames:]
	}
	if len(out) == 0 {
		return nil
	}
	return &Stacktrace{Frames: out}
}

func inApp(f runtime.Frame) bool {
	return !strings.Contains(f.File, "/pkg/mod/") && !strings.HasPrefix(f.File, runtime.GOROOT()) && !strings.HasPrefix(f.Function, "runtime.") && !strings.HasPrefix(f.Function, "testing.")
}

func crumbOf(b *Breadcrumb) crumb {
	at := b.Timestamp
	if at.IsZero() {
		at = time.Now()
	}
	data := scrubMap(b.Data)
	category := b.Category
	if !crumbKinds[category] {
		if category != "" {
			if data == nil {
				data = map[string]any{}
			}
			data["category"] = cut(category, 256)
		}
		category = "custom"
	}
	level := b.Level
	if level == LevelFatal {
		level = LevelError
	}
	if level != "" && level != LevelError && level != LevelWarning && level != LevelInfo && level != LevelDebug {
		level = LevelInfo
	}
	return crumb{Timestamp: at.UTC().Format(time.RFC3339Nano), Category: category, Message: cut(b.Message, 1024), Level: level, Data: data}
}

func scrubMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		switch inner := v.(type) {
		case map[string]any:
			out[k] = scrubMap(inner)
		default:
			out[k] = v
		}
		if sensitiveKey.MatchString(k) && v != nil && v != "" {
			out[k] = filtered
		}
	}
	return out
}

func scrubURL(raw string) string {
	base, query, found := strings.Cut(raw, "?")
	if !found {
		return cut(raw, 2048)
	}
	pairs := strings.Split(query, "&")
	for i, pair := range pairs {
		if name, _, ok := strings.Cut(pair, "="); ok && sensitiveParam.MatchString(name) {
			pairs[i] = name + "=[redacted]"
		}
	}
	return cut(base+"?"+strings.Join(pairs, "&"), 2048)
}

func requestOf(r *http.Request) *Request {
	if r == nil {
		return nil
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	headers := map[string]string{}
	for _, h := range keptHeaders {
		if v := r.Header.Get(h); v != "" {
			headers[strings.ToLower(h)] = cut(v, 1024)
		}
	}
	return &Request{URL: scrubURL(scheme + "://" + r.Host + r.URL.RequestURI()), Method: cut(r.Method, 16), Headers: headers}
}

func tagKey(key string) string {
	return cut(badTagChar.ReplaceAllString(key, "_"), 64)
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8Start(s[n]) {
		n--
	}
	return s[:n]
}

func utf8Start(b byte) bool {
	return b&0xC0 != 0x80
}

func newID(bytes int) string {
	b := make([]byte, bytes)
	rand.Read(b)
	return hex.EncodeToString(b)
}
