package blipit

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func sendTwice(t *testing.T, environment, header string) (string, []byte) {
	t.Helper()
	var first []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reader io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			reader, _ = gzip.NewReader(r.Body)
		}
		body, _ := io.ReadAll(reader)
		if first == nil {
			first = body
		}
		if header != "" {
			w.Header().Set("X-Blipit-Latest", header)
		}
		w.Write([]byte("{}"))
	}))
	defer srv.Close()
	noticed.Store(false)
	var out bytes.Buffer
	log.SetOutput(&out)
	defer log.SetOutput(io.Discard)
	if err := Init(Options{Key: "blipit_pk_abc", Project: "7", Environment: environment, Endpoint: srv.URL}); err != nil {
		t.Fatal(err)
	}
	CaptureMessage("one")
	Flush(5 * time.Second)
	CaptureMessage("two")
	Flush(5 * time.Second)
	return out.String(), first
}

const latest = `{"version":"0.2.9","severity":"normal","note":"Faster."}`

func TestIdentityPackage(t *testing.T) {
	_, body := sendTwice(t, "test", "")
	lines := strings.Split(string(body), "\n")
	var event struct {
		Sdk struct {
			Packages []struct{ Name, Version string } `json:"packages"`
		} `json:"sdk"`
	}
	if err := json.Unmarshal([]byte(lines[2]), &event); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range event.Sdk.Packages {
		found = found || (p.Name == "go:github.com/blipit-io/blipit-go" && p.Version == Version)
	}
	if !found {
		t.Fatalf("packages = %+v", event.Sdk.Packages)
	}
}

func TestNoticeOnceInDevelopment(t *testing.T) {
	out, _ := sendTwice(t, "development", url.PathEscape(latest))
	want := "[blipit] 0.2.9 is available (you have " + Version + ") — Faster. — go get github.com/blipit-io/blipit-go@latest"
	if strings.Count(out, "is available") != 1 || !strings.Contains(out, want) {
		t.Fatalf("log = %q", out)
	}
}

func TestNoNoticeInProduction(t *testing.T) {
	if out, _ := sendTwice(t, "production", url.PathEscape(latest)); strings.Contains(out, "is available") {
		t.Fatalf("log = %q", out)
	}
}

func TestMalformedHeaderIsSilent(t *testing.T) {
	for _, h := range []string{"", "not%20json", url.PathEscape(`{"version":3}`), "%zz"} {
		if out, _ := sendTwice(t, "development", h); strings.Contains(out, "is available") {
			t.Fatalf("header %q logged %q", h, out)
		}
	}
	if got := updateNotice(url.PathEscape(`{"version":"0.2.9","note":""}`), "0.2.1"); got != "[blipit] 0.2.9 is available (you have 0.2.1) — go get github.com/blipit-io/blipit-go@latest" {
		t.Fatalf("got %q", got)
	}
}
