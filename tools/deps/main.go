// Command deps is the dependency inventory and vulnerability check.
//
//	go run ./tools/deps          inventory: run when a stage starts and again before the stage hand-over
//	go run ./tools/deps -audit   known vulnerabilities: fails the stage hand-over
//
// The inventory lists every direct module with a newer stable version that is at
// least minAgeDays old (the same age as Dependabot's cooldown in
// .github/dependabot.yml), and the newest Go release of that age against the go
// line of go.mod. It only reports; what to upgrade is decided in the stage plan.
// A new major version of a Go module is a different module path (/v2), so it
// does not show up here; check the module's own releases for one.
//
// The audit runs govulncheck (pinned below) over ./... and exits 1 on a
// vulnerability the code calls that is not listed in tools/deps/exceptions.json,
// or on an exception past its reviewBy date; vulnerabilities in modules the code
// does not call are listed but do not fail. Exit 2 means the check could not run
// (no network, govulncheck failed): record it as not run, never as passed.
//
// Standard library only; govulncheck is fetched by go run.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	minAgeDays = 7
	userAgent  = "dependency-inventory"
)

var client = &http.Client{Timeout: 30 * time.Second}

type module struct {
	Path     string
	Version  string
	Main     bool
	Indirect bool
	Replace  *module
}

func main() {
	audit := flag.Bool("audit", false, "check known vulnerabilities instead of listing updates")
	flag.Parse()
	var code int
	var err error
	if *audit {
		code, err = runAudit()
	} else {
		code, err = runInventory()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "[deps] not run: %v\n", err)
		check := "dependency-inventory"
		if *audit {
			check = "dependency-audit"
		}
		fmt.Printf("{\"status\":\"not-run\",\"check\":%q}\n", check)
		os.Exit(2)
	}
	os.Exit(code)
}

// buildList is `go list -m -json all`: the main module and every module in the build.
func buildList() ([]module, error) {
	cmd := exec.Command("go", "list", "-m", "-json", "all")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list -m all: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var mods []module
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var m module
		if err := dec.Decode(&m); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		mods = append(mods, m)
	}
	return mods, nil
}

// goLine is the version on the go line of go.mod, e.g. "1.27.3".
func goLine() (string, error) {
	f, err := os.Open("go.mod")
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if fields := strings.Fields(sc.Text()); len(fields) == 2 && fields[0] == "go" {
			return fields[1], nil
		}
	}
	return "", errors.New("go.mod has no go line")
}

func getJSON(url string, v any) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func getText(url string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

// escapePath is the module proxy's case encoding: an upper-case letter becomes "!" and its lower case.
func escapePath(path string) string {
	var b strings.Builder
	for _, r := range path {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('!')
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// stable parses "v1.2.3" (or "1.2.3"); prereleases, pseudo-versions and +incompatible are not stable.
func stable(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func less(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

type candidate struct {
	version string
	days    int
}

// newestMature is the newest stable version above current that is at least minAgeDays old,
// and the newest stable version of any age.
func newestMature(path, current string, info func(version string) (time.Time, error)) (mature, newest *candidate, err error) {
	cur, ok := stable(current)
	if !ok {
		cur = [3]int{}
	}
	list, err := getText("https://proxy.golang.org/" + escapePath(path) + "/@v/list")
	if err != nil {
		return nil, nil, err
	}
	var versions []string
	for _, v := range strings.Fields(list) {
		if p, ok := stable(v); ok && less(cur, p) {
			versions = append(versions, v)
		}
	}
	sort.Slice(versions, func(i, j int) bool {
		a, _ := stable(versions[i])
		b, _ := stable(versions[j])
		return less(b, a)
	})
	for _, v := range versions {
		at, err := info(v)
		if err != nil {
			return nil, nil, err
		}
		c := &candidate{version: v, days: int(time.Since(at).Hours() / 24)}
		if newest == nil {
			newest = c
		}
		if c.days >= minAgeDays {
			return c, newest, nil
		}
	}
	return nil, newest, nil
}

func moduleTime(path string) func(string) (time.Time, error) {
	return func(version string) (time.Time, error) {
		var info struct{ Time time.Time }
		err := getJSON("https://proxy.golang.org/"+escapePath(path)+"/@v/"+version+".info", &info)
		return info.Time, err
	}
}

type row struct {
	name, current string
	mature        *candidate
	newest        *candidate
}

func runInventory() (int, error) {
	mods, err := buildList()
	if err != nil {
		return 0, err
	}
	var direct []module
	for _, m := range mods {
		if !m.Main && !m.Indirect && m.Replace == nil {
			direct = append(direct, m)
		}
	}
	rows := make([]row, len(direct))
	errs := make([]error, len(direct))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for i, m := range direct {
		wg.Add(1)
		go func(i int, m module) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			mature, newest, err := newestMature(m.Path, m.Version, moduleTime(m.Path))
			rows[i], errs[i] = row{name: m.Path, current: m.Version, mature: mature, newest: newest}, err
		}(i, m)
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return 0, err
	}

	// The Go toolchain: the newest release at least minAgeDays old, from the proxy's toolchain module.
	line, err := goLine()
	if err != nil {
		return 0, err
	}
	var releases []struct {
		Version string
		Stable  bool
	}
	if err := getJSON("https://go.dev/dl/?mode=json", &releases); err != nil {
		return 0, err
	}
	goRow := row{name: "go (go.mod go line)", current: line}
	cur, _ := stable(line)
	for _, r := range releases {
		v := strings.TrimPrefix(r.Version, "go")
		p, ok := stable(v)
		if !r.Stable || !ok || !less(cur, p) {
			continue
		}
		at, err := moduleTime("golang.org/toolchain")("v0.0.1-go" + v + ".windows-amd64")
		if err != nil {
			return 0, err
		}
		c := &candidate{version: v, days: int(time.Since(at).Hours() / 24)}
		if goRow.newest == nil {
			goRow.newest = c
		}
		if c.days >= minAgeDays {
			goRow.mature = c
			break
		}
	}
	rows = append(rows, goRow)

	due, waiting := 0, 0
	fmt.Printf("[deps] newer stable versions at least %d days old (direct modules and the go line):\n", minAgeDays)
	for _, r := range rows {
		if r.mature != nil {
			due++
			fmt.Printf("  %s %s -> %s (%d days)\n", r.name, r.current, r.mature.version, r.mature.days)
		}
	}
	if due == 0 {
		fmt.Println("  none")
	}
	for _, r := range rows {
		if r.newest != nil && (r.mature == nil || r.mature.version != r.newest.version) {
			if waiting == 0 {
				fmt.Printf("[deps] newer, but younger than %d days (not due yet):\n", minAgeDays)
			}
			waiting++
			fmt.Printf("  %s %s -> %s (%d days)\n", r.name, r.current, r.newest.version, r.newest.days)
		}
	}
	fmt.Printf("{\"status\":\"reported\",\"check\":\"dependency-inventory\",\"due\":%d,\"waiting\":%d}\n", due, waiting)
	return 0, nil
}

type exception struct {
	Advisory string `json:"advisory"`
	Module   string `json:"module"`
	Reason   string `json:"reason"`
	ReviewBy string `json:"reviewBy"`
}

// govulncheck is the Go team's vulnerability scanner. It reports a vulnerability only when the code calls it,
// unlike a module-version lookup that also flags packages TRACE never imports. Pinned; v1.1.x crashes on Go 1.27.
const govulncheck = "golang.org/x/vuln/cmd/govulncheck@v1.9.0"

type osvEntry struct {
	ID      string   `json:"id"`
	Summary string   `json:"summary"`
	Aliases []string `json:"aliases"`
}

type govulnMessage struct {
	OSV     *osvEntry `json:"osv"`
	Finding *struct {
		OSV          string `json:"osv"`
		FixedVersion string `json:"fixed_version"`
		Trace        []struct {
			Module   string `json:"module"`
			Version  string `json:"version"`
			Package  string `json:"package"`
			Function string `json:"function"`
		} `json:"trace"`
	} `json:"finding"`
}

func runAudit() (int, error) {
	raw, err := os.ReadFile("tools/deps/exceptions.json")
	if err != nil {
		return 0, err
	}
	var file struct {
		Exceptions []exception `json:"exceptions"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return 0, fmt.Errorf("tools/deps/exceptions.json: %w", err)
	}
	cmd := exec.Command("go", "run", govulncheck, "-format", "json", "./...")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("govulncheck: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	entries := map[string]*osvEntry{}
	type hit struct{ module, version, fixed string }
	called := map[string]hit{}    // a function on the call path: the code reaches it
	imported := map[string]bool{} // in a required module or imported package, but not called
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var m govulnMessage
		if err := dec.Decode(&m); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return 0, fmt.Errorf("govulncheck output: %w", err)
		}
		if m.OSV != nil {
			entries[m.OSV.ID] = m.OSV
		}
		if f := m.Finding; f != nil && len(f.Trace) > 0 {
			if f.Trace[0].Function != "" {
				called[f.OSV] = hit{f.Trace[0].Module, f.Trace[0].Version, f.FixedVersion}
			} else {
				imported[f.OSV] = true
			}
		}
	}

	today := time.Now().Format("2006-01-02")
	matches := func(id string, e exception) bool {
		if e.Advisory == id {
			return true
		}
		if entry := entries[id]; entry != nil {
			for _, a := range entry.Aliases {
				if a == e.Advisory {
					return true
				}
			}
		}
		return false
	}
	ids := make([]string, 0, len(called))
	for id := range called {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	open, expired := 0, 0
	used := map[string]bool{}
	for _, id := range ids {
		excepted := false
		for _, e := range file.Exceptions {
			if matches(id, e) {
				excepted, used[e.Advisory] = true, true
			}
		}
		if !excepted {
			open++
			h, summary := called[id], ""
			if entries[id] != nil {
				summary = entries[id].Summary
			}
			fixed := h.fixed
			if fixed == "" {
				fixed = "no release yet"
			}
			fmt.Fprintf(os.Stderr, "[deps] %s %s@%s (called): %s; fixed in %s\n", id, h.module, h.version, summary, fixed)
		}
	}
	notCalled := 0
	for id := range imported {
		if _, ok := called[id]; !ok {
			notCalled++
		}
	}
	if notCalled > 0 {
		fmt.Printf("[deps] %d more in required modules that the code does not call (not a failure; upgrading the module clears them; details: go run %s -show verbose ./...)\n", notCalled, govulncheck)
	}
	for _, e := range file.Exceptions {
		if e.ReviewBy == "" || e.ReviewBy < today {
			expired++
			fmt.Fprintf(os.Stderr, "[deps] exception %s (%s) passed its reviewBy date %q\n", e.Advisory, e.Module, e.ReviewBy)
		}
		if !used[e.Advisory] {
			fmt.Printf("[deps] exception %s (%s) no longer matches a called vulnerability: remove it\n", e.Advisory, e.Module)
		}
	}
	status := "passed"
	if open > 0 || expired > 0 {
		status = "failed"
	}
	fmt.Printf("{\"status\":%q,\"check\":\"dependency-audit\",\"called\":%d,\"excepted\":%d,\"open\":%d,\"notCalled\":%d,\"expiredExceptions\":%d}\n",
		status, len(called), len(called)-open, open, notCalled, expired)
	if status == "failed" {
		return 1, nil
	}
	return 0, nil
}
