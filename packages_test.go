package blipit

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPackagesListStdlibAndAreSentOnce(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	list := installedPackages()
	if !containsStdlib(list) {
		t.Fatalf("stdlib missing: %+v", list)
	}
	for _, p := range list {
		if p.Ecosystem != "Go" || p.Name == "" || strings.HasPrefix(p.Version, "v") {
			t.Fatalf("bad package %+v", p)
		}
	}
	in := newIngest(t, nil)
	key := "blipit_sk_go_packages"
	if n := reportPackages(in.URL, key); n != len(list) {
		t.Fatalf("sent %d, want %d", n, len(list))
	}
	if n := reportPackages(in.URL, key); n != 0 {
		t.Fatalf("unchanged list sent again (%d)", n)
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	if len(in.received) != 1 || in.received[0].path != "/api/v1/sdk/packages" || in.received[0].key != key {
		t.Fatalf("got %+v", in.received)
	}
	var body struct{ Packages []Package }
	if err := json.Unmarshal(in.received[0].body, &body); err != nil || len(body.Packages) != len(list) {
		t.Fatalf("body %s", in.received[0].body)
	}
	if !containsVersion(body.Packages, strings.TrimPrefix(runtime.Version(), "go")) {
		t.Fatalf("stdlib version missing in %s", in.received[0].body)
	}
}

func TestInitSchedulesPackagesInTheBackground(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	old := packagesDelay
	packagesDelay = 50 * time.Millisecond
	defer func() { packagesDelay = old }()
	in := newIngest(t, nil)
	if err := Init(Options{Key: "blipit_sk_go_init", Project: "1", Endpoint: in.URL}); err != nil {
		t.Fatal(err)
	}
	if schedulePackages(in.URL, "blipit_pk_public") {
		t.Fatal("a public key must not send the package list")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		in.mu.Lock()
		n := len(in.received)
		in.mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("package list never arrived")
}

func containsStdlib(list []Package) bool {
	for _, p := range list {
		if p.Name == "stdlib" {
			return true
		}
	}
	return false
}

func containsVersion(list []Package, version string) bool {
	for _, p := range list {
		if p.Name == "stdlib" && p.Version == version {
			return true
		}
	}
	return false
}
