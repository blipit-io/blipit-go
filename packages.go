package blipit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"time"
)

const maxPackages = 3000

var packagesDelay = 10 * time.Second

type Package struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	Version   string `json:"version"`
}

func installedPackages() []Package {
	out := []Package{{Ecosystem: "Go", Name: "stdlib", Version: strings.TrimPrefix(runtime.Version(), "go")}}
	info, ok := debug.ReadBuildInfo()
	if ok {
		for _, dep := range info.Deps {
			if dep.Replace != nil {
				dep = dep.Replace
			}
			version := strings.TrimPrefix(dep.Version, "v")
			if dep.Path == "" || version == "" || version == "(devel)" {
				continue
			}
			out = append(out, Package{Ecosystem: "Go", Name: dep.Path, Version: version})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].Version < out[j].Version
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > maxPackages {
		out = out[:maxPackages]
	}
	return out
}

func digest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func reportPackages(endpoint, key string) int {
	packages := installedPackages()
	lines := make([]string, len(packages))
	for i, p := range packages {
		lines[i] = p.Name + "@" + p.Version
	}
	stamp := digest(strings.Join(lines, "\n"))
	marker := filepath.Join(os.TempDir(), "blipit-packages-"+digest(key)[:16])
	if old, err := os.ReadFile(marker); err == nil && string(old) == stamp {
		return 0
	}
	body, err := json.Marshal(map[string][]Package{"packages": packages})
	if err != nil {
		return 0
	}
	req, err := http.NewRequest(http.MethodPut, endpoint+"/api/v1/sdk/packages", bytes.NewReader(body))
	if err != nil {
		return 0
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Blipit-Key", key)
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return 0
	}
	res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return 0
	}
	_ = os.WriteFile(marker, []byte(stamp), 0o600)
	return len(packages)
}

func schedulePackages(endpoint, key string) bool {
	if strings.HasPrefix(key, "blipit_pk_") {
		return false
	}
	go func() {
		defer func() { _ = recover() }()
		time.Sleep(packagesDelay)
		reportPackages(endpoint, key)
	}()
	return true
}
