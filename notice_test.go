package blipit

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

const latest = `{"version":"0.3.9","severity":"normal","note":"Faster."}`

func sendTwice(t *testing.T, environment, latestJSON string) string {
	t.Helper()
	in := newIngest(t, func(n int, w http.ResponseWriter) {
		w.WriteHeader(http.StatusAccepted)
		if latestJSON == "" {
			w.Write([]byte(`{"id":"x"}`))
			return
		}
		w.Write([]byte(`{"id":"x","latest":` + latestJSON + `}`))
	})
	noticed.Store(false)
	var out bytes.Buffer
	log.SetOutput(&out)
	defer log.SetOutput(os.Stderr)
	start(t, in, Options{Environment: environment})
	CaptureMessage("one")
	Flush(5 * time.Second)
	CaptureMessage("two")
	Flush(5 * time.Second)
	return out.String()
}

func TestNoticeOnceInDevelopment(t *testing.T) {
	out := sendTwice(t, "development", latest)
	want := "[blipit] 0.3.9 is available (you have " + Version + ") — Faster. — go get github.com/blipit-io/blipit-go@latest"
	if strings.Count(out, "is available") != 1 || !strings.Contains(out, want) {
		t.Fatalf("log = %q", out)
	}
}

func TestNoNoticeInProduction(t *testing.T) {
	if out := sendTwice(t, "production", latest); strings.Contains(out, "is available") {
		t.Fatalf("log = %q", out)
	}
}

func TestMalformedLatestIsSilent(t *testing.T) {
	for _, l := range []string{"", `"text"`, `{"version":3}`, `{"version":"` + Version + `"}`} {
		if out := sendTwice(t, "development", l); strings.Contains(out, "is available") {
			t.Fatalf("latest %q logged %q", l, out)
		}
	}
	if got := updateNotice(json.RawMessage(`{"version":"0.3.9","note":""}`), "0.3.0"); got != "[blipit] 0.3.9 is available (you have 0.3.0) — go get github.com/blipit-io/blipit-go@latest" {
		t.Fatalf("got %q", got)
	}
}
