package blipit

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getsentry/sentry-go"
)

const DefaultEndpoint = "https://in.blipit.io"

const Version = "0.2.1"

const updateCommand = "go get github.com/blipit-io/blipit-go@latest"

const publicKeyWarning = "[blipit] login attempts need the project's secret key (blipit_sk_...). This SDK was started with the public key, so CaptureSecurity sends nothing. Use the secret key on the server."

var (
	ErrNoKey     = errors.New("blipit: Init got no key, so Blipit is off and nothing is sent; pass the project's key from app.blipit.io, Keys")
	ErrNoProject = errors.New("blipit: Init got no project id, so Blipit is off and nothing is sent; pass the id from the project's Keys page at app.blipit.io")
)

var (
	publicKey       atomic.Bool
	publicKeyWarned sync.Once
	noticed         atomic.Bool
)

type Options struct {
	Key              string
	Project          string
	Environment      string
	Release          string
	TracesSampleRate float64
	Endpoint         string
	Debug            bool
}

type User = sentry.User

type Breadcrumb = sentry.Breadcrumb

type Security struct {
	Kind      string
	Actor     string
	Outcome   string
	ActorID   string
	IP        string
	UserAgent string
	Target    string
}

func DSN(key, project, endpoint string) string {
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	scheme := "https"
	if strings.HasPrefix(endpoint, "http://") {
		scheme = "http"
	}
	host := endpoint
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	return fmt.Sprintf("%s://%s@%s/%s", scheme, key, strings.TrimRight(host, "/"), project)
}

func Init(o Options) error {
	if strings.TrimSpace(o.Key) == "" {
		return ErrNoKey
	}
	if strings.TrimSpace(o.Project) == "" {
		return ErrNoProject
	}
	publicKey.Store(strings.HasPrefix(o.Key, "blipit_pk_"))
	options := sentry.ClientOptions{
		Dsn:              DSN(o.Key, o.Project, o.Endpoint),
		Environment:      o.Environment,
		Release:          o.Release,
		TracesSampleRate: o.TracesSampleRate,
		SendDefaultPII:   false,
		Debug:            o.Debug,
		BeforeSend:       withIdentity,
	}
	if o.Debug || o.Environment == "development" {
		options.HTTPTransport = noticeTransport{next: http.DefaultTransport}
	}
	return sentry.Init(options)
}

func withIdentity(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	for _, p := range event.Sdk.Packages {
		if p.Name == "go:github.com/blipit-io/blipit-go" {
			return event
		}
	}
	event.Sdk.Packages = append(event.Sdk.Packages, sentry.SdkPackage{Name: "go:github.com/blipit-io/blipit-go", Version: Version})
	return event
}

type noticeTransport struct {
	next http.RoundTripper
}

func (t noticeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := t.next.RoundTrip(r)
	if err == nil && res != nil && !noticed.Load() {
		if line := updateNotice(res.Header.Get("X-Blipit-Latest"), Version); line != "" && noticed.CompareAndSwap(false, true) {
			log.Print(line)
		}
	}
	return res, err
}

func updateNotice(header, current string) string {
	if header == "" {
		return ""
	}
	raw, err := url.PathUnescape(header)
	if err != nil {
		return ""
	}
	var latest struct {
		Version any `json:"version"`
		Note    any `json:"note"`
	}
	if json.Unmarshal([]byte(raw), &latest) != nil {
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

func CaptureException(err error) {
	sentry.CaptureException(err)
}

func CaptureMessage(message string) {
	sentry.CaptureMessage(message)
}

func SetUser(user User) {
	sentry.ConfigureScope(func(scope *sentry.Scope) { scope.SetUser(user) })
}

func SetTag(key, value string) {
	sentry.ConfigureScope(func(scope *sentry.Scope) { scope.SetTag(key, value) })
}

func AddBreadcrumb(breadcrumb *Breadcrumb) {
	sentry.AddBreadcrumb(breadcrumb)
}

func CaptureSecurity(s Security) {
	if publicKey.Load() {
		publicKeyWarned.Do(func() { log.Print(publicKeyWarning) })
		return
	}
	context := map[string]any{"kind": s.Kind, "actor": s.Actor}
	optional := map[string]string{
		"outcome":    s.Outcome,
		"actor_id":   s.ActorID,
		"ip":         s.IP,
		"user_agent": s.UserAgent,
		"target":     s.Target,
	}
	for k, v := range optional {
		if v != "" {
			context[k] = v
		}
	}
	level := sentry.LevelInfo
	if s.Kind == "login_failed" || s.Kind == "login_blocked" {
		level = sentry.LevelWarning
	}
	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetContext("security", context)
		scope.SetTag("security.kind", s.Kind)
		scope.SetLevel(level)
		sentry.CaptureMessage(fmt.Sprintf("%s for %s", s.Kind, s.Actor))
	})
}

func Flush(timeout time.Duration) bool {
	return sentry.Flush(timeout)
}
