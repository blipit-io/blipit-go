package blipit

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"
)

const DefaultEndpoint = "https://in.blipit.io"

const Version = "0.3.1"

const sdkName = "blipit-go"

const updateCommand = "go get github.com/blipit-io/blipit-go@latest"

const publicKeyWarning = "[blipit] login attempts need the project's secret key (blipit_sk_...). This SDK was started with the public key, so CaptureSecurity sends nothing. Use the secret key on the server."

var (
	ErrNoKey     = errors.New("blipit: Init got no key, so Blipit is off and nothing is sent; pass the project's key from app.blipit.io, Keys")
	ErrNoProject = errors.New("blipit: Init got no project id, so Blipit is off and nothing is sent; pass the id from the project's Keys page at app.blipit.io")
	ErrEndpoint  = errors.New("blipit: Init got an Endpoint that is not an http(s) URL, so Blipit is off")
)

type Options struct {
	Key              string
	Project          string
	Environment      string
	Release          string
	TracesSampleRate float64
	Endpoint         string
	Debug            bool
	ServerName       string
	BeforeSend       func(*Event) *Event
}

type Level string

const (
	LevelDebug   Level = "debug"
	LevelInfo    Level = "info"
	LevelWarning Level = "warning"
	LevelError   Level = "error"
	LevelFatal   Level = "fatal"
)

type User struct {
	ID       string `json:"id,omitempty"`
	Email    string `json:"email,omitempty"`
	Username string `json:"username,omitempty"`
}

type Breadcrumb struct {
	Category  string
	Message   string
	Level     Level
	Data      map[string]any
	Timestamp time.Time
}

type Security struct {
	Kind      string `json:"kind"`
	Actor     string `json:"actor"`
	Outcome   string `json:"outcome,omitempty"`
	ActorID   string `json:"actor_id,omitempty"`
	IP        string `json:"ip,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`
	Target    string `json:"target,omitempty"`
	Database  string `json:"database,omitempty"`
}

var securityText = map[string]string{
	"login_failed":             "Failed login",
	"login_succeeded":          "Successful login",
	"login_blocked":            "Blocked login",
	"password_reset_requested": "Password reset requested",
	"privilege_granted":        "Privilege granted",
}

var publicKeyWarned sync.Once

func Init(o Options) error {
	current.Store(nil)
	if strings.TrimSpace(o.Key) == "" {
		return ErrNoKey
	}
	if strings.TrimSpace(o.Project) == "" {
		return ErrNoProject
	}
	if o.Endpoint == "" {
		o.Endpoint = DefaultEndpoint
	}
	u, err := url.Parse(o.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ErrEndpoint
	}
	o.Endpoint = strings.TrimRight(o.Endpoint, "/")
	if o.Environment == "" {
		o.Environment = "production"
	}
	current.Store(newClient(o))
	return nil
}

func CaptureException(err error) {
	if c := current.Load(); c != nil && err != nil {
		c.send(&Event{Level: LevelError, Exception: exceptionOf(err, 1, true)})
	}
}

func CaptureMessage(message string) {
	if c := current.Load(); c != nil {
		c.send(&Event{Level: LevelInfo, Message: cut(message, 8192)})
	}
}

func SetUser(user User) {
	scope.Lock()
	scope.user = &user
	scope.Unlock()
}

func SetTag(key, value string) {
	scope.Lock()
	scope.tags[tagKey(key)] = cut(value, 256)
	scope.Unlock()
}

func AddBreadcrumb(breadcrumb *Breadcrumb) {
	if breadcrumb == nil {
		return
	}
	scope.Lock()
	scope.crumbs = append(scope.crumbs, crumbOf(breadcrumb))
	if len(scope.crumbs) > maxCrumbs {
		scope.crumbs = scope.crumbs[len(scope.crumbs)-maxCrumbs:]
	}
	scope.Unlock()
}

func CaptureSecurity(s Security) {
	c := current.Load()
	if c == nil {
		return
	}
	if strings.HasPrefix(c.opts.Key, "blipit_pk_") {
		publicKeyWarned.Do(func() { log.Print(publicKeyWarning) })
		return
	}
	text, known := securityText[s.Kind]
	s.Actor = cut(strings.TrimSpace(s.Actor), 256)
	if !known || s.Actor == "" {
		c.debugf("[blipit] security event dropped, needs a known kind and an actor")
		return
	}
	s.ActorID, s.IP, s.UserAgent = cut(s.ActorID, 128), cut(strings.TrimSpace(s.IP), 45), cut(s.UserAgent, 512)
	s.Outcome, s.Target, s.Database = cut(s.Outcome, 64), cut(s.Target, 256), cut(s.Database, 64)
	level := LevelInfo
	if s.Kind == "login_failed" || s.Kind == "login_blocked" || s.Kind == "privilege_granted" {
		level = LevelWarning
	}
	c.send(&Event{Level: level, Message: fmt.Sprintf("%s for %s", text, s.Actor), Security: &s})
}

func Recover() {
	if v := recover(); v != nil {
		capturePanic(v, nil)
		Flush(2 * time.Second)
		panic(v)
	}
}

func Flush(timeout time.Duration) bool {
	c := current.Load()
	if c == nil {
		return true
	}
	return c.flush(timeout)
}
