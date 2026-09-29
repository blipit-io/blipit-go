package blipit

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type request struct {
	path string
	body []byte
}

func TestEventsReachBlipit(t *testing.T) {
	var mu sync.Mutex
	var received []request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reader io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			gz, err := gzip.NewReader(r.Body)
			if err != nil {
				t.Errorf("gzip: %v", err)
			}
			reader = gz
		}
		body, _ := io.ReadAll(reader)
		mu.Lock()
		received = append(received, request{r.URL.Path, body})
		mu.Unlock()
		w.Write([]byte("{}"))
	}))
	defer srv.Close()

	if got := DSN("blipit_pk_abc", "7", ""); got != "https://blipit_pk_abc@in.blipit.io/7" {
		t.Fatalf("dsn = %q", got)
	}
	if err := Init(Options{Project: "7"}); err == nil {
		t.Fatal("Init without a key should fail")
	}
	if err := Init(Options{Key: "blipit_pk_abc", Project: "7", Environment: "test", Endpoint: srv.URL}); err != nil {
		t.Fatal(err)
	}
	CaptureMessage("hello from go")
	if !Flush(5 * time.Second) {
		t.Fatal("flush timed out")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) == 0 {
		t.Fatal("nothing was sent")
	}
	if received[0].path != "/api/7/envelope/" {
		t.Fatalf("path = %q", received[0].path)
	}
	lines := strings.Split(string(received[0].body), "\n")
	if len(lines) < 3 {
		t.Fatalf("envelope has %d lines", len(lines))
	}
	var event struct {
		Message     string `json:"message"`
		Environment string `json:"environment"`
	}
	if err := json.Unmarshal([]byte(lines[2]), &event); err != nil {
		t.Fatalf("event json: %v\n%s", err, lines[2])
	}
	if event.Message != "hello from go" {
		t.Fatalf("message = %q", event.Message)
	}
	if event.Environment != "test" {
		t.Fatalf("environment = %q", event.Environment)
	}
}
