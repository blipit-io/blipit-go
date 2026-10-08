package blipit

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getsentry/sentry-go"
)

const DefaultEndpoint = "https://in.blipit.io"

const publicKeyWarning = "[blipit] login attempts need the project's secret key (blipit_sk_...). This SDK was started with the public key, so CaptureSecurity sends nothing. Use the secret key on the server."

var (
	publicKey       atomic.Bool
	publicKeyWarned sync.Once
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
	if o.Key == "" {
		return errors.New("blipit: Init needs the project's public key")
	}
	if o.Project == "" {
		return errors.New("blipit: Init needs the project id")
	}
	publicKey.Store(strings.HasPrefix(o.Key, "blipit_pk_"))
	return sentry.Init(sentry.ClientOptions{
		Dsn:              DSN(o.Key, o.Project, o.Endpoint),
		Environment:      o.Environment,
		Release:          o.Release,
		TracesSampleRate: o.TracesSampleRate,
		SendDefaultPII:   false,
		Debug:            o.Debug,
	})
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
