// Package tests runs seriesdiff the way the skill and the README do: as a
// single Go file, on saved scrapes, and on a live example service.
package tests

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "seriesdiff-bin")
	if err != nil {
		panic(err)
	}
	bin = filepath.Join(dir, "seriesdiff")
	// Build the single file, as the skill runs it.
	out, err := exec.Command("go", "build", "-o", bin, "../plugin/scripts/seriesdiff.go").CombinedOutput()
	if err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func seriesdiff(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = ".."
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return code, out.String(), errOut.String()
}

// readmeExample is one `$ command` in the README and the output under it.
type readmeExample struct {
	line int
	cmd  string
	want string
}

func readmeExamples(t *testing.T) []readmeExample {
	f, err := os.Open("../README.md")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	var examples []readmeExample
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "    $ ") {
			continue
		}
		ex := readmeExample{line: i + 1, cmd: strings.TrimPrefix(lines[i], "    $ ")}
		var out []string
		j := i + 1
		for ; j < len(lines); j++ {
			l := lines[j]
			if strings.HasPrefix(l, "    $ ") || (l != "" && !strings.HasPrefix(l, "    ")) {
				break
			}
			out = append(out, strings.TrimPrefix(l, "    "))
		}
		for len(out) > 0 && out[len(out)-1] == "" {
			out = out[:len(out)-1]
		}
		ex.want = strings.Join(out, "\n")
		examples = append(examples, ex)
		i = j - 1
	}
	return examples
}

// TestReadmeExamples runs every `$ ` line in the README from the repository
// root and checks that the output shown under it is what the command prints.
func TestReadmeExamples(t *testing.T) {
	examples := readmeExamples(t)
	if len(examples) < 5 {
		t.Fatalf("only %d README examples found", len(examples))
	}
	for _, ex := range examples {
		t.Run(fmt.Sprintf("line%d", ex.line), func(t *testing.T) {
			cmd := exec.Command("sh", "-c", ex.cmd)
			cmd.Dir = ".."
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("README line %d: %s: %v\n%s", ex.line, ex.cmd, err, out)
			}
			got := strings.TrimRight(string(out), "\n")
			if got != ex.want {
				t.Fatalf("README line %d: %s\n--- README\n%s\n--- actual\n%s", ex.line, ex.cmd, ex.want, got)
			}
		})
	}
}

func TestExamplesFromTheBinary(t *testing.T) {
	code, out, _ := seriesdiff(t, "compare", "--fail", "--refs", "examples/monitoring",
		"examples/scrapes/orders-before.prom", "examples/scrapes/orders-after.prom")
	if code != 1 || !strings.Contains(out, "probably renamed to orders_queue_depth") ||
		!strings.Contains(out, "used in examples/monitoring/alerts.yml:11") {
		t.Fatalf("code %d\n%s", code, out)
	}
	code, out, _ = seriesdiff(t, "compare", "--fail", "examples/scrapes/orders-before.prom", "examples/scrapes/orders-comments.prom")
	if code != 0 || !strings.HasPrefix(out, "No changes") {
		t.Fatalf("comments: code %d\n%s", code, out)
	}
	// Real releases: each change below is in the project's changelog.
	code, out, _ = seriesdiff(t, "compare", "--fail", "examples/scrapes/real/node_exporter-1.10.1.prom", "examples/scrapes/real/node_exporter-1.10.2.prom")
	if code != 1 || !strings.Contains(out, "node_memory_Zswap_ytes  gauge, 1 series\n      probably renamed to node_memory_Zswap_bytes") {
		t.Fatalf("node_exporter 1.10.2: code %d\n%s", code, out)
	}
	code, out, _ = seriesdiff(t, "compare", "--fail", "examples/scrapes/real/prometheus-2.55.1.prom", "examples/scrapes/real/prometheus-3.0.1.prom")
	if code != 1 || !strings.Contains(out, `handler no longer has "/starting", "/static/*filepath"`) {
		t.Fatalf("prometheus 3.0.1: code %d\n%s", code, out)
	}
	code, out, _ = seriesdiff(t, "compare", "--fail", "examples/scrapes/real/node_exporter-1.7.0.prom", "examples/scrapes/real/node_exporter-1.7.0-again.prom")
	if code != 0 || !strings.HasPrefix(out, "No changes") {
		t.Fatalf("same version twice: code %d\n%s", code, out)
	}
}

// TestLiveService builds the example service, runs each version with the same
// requests, scrapes /metrics over HTTP and compares the scrapes. It needs
// client_golang in the module cache, so it's skipped when that isn't there
// and the network is off.
func TestLiveService(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	dir := t.TempDir()
	scrapes := map[string]string{}
	for _, v := range []string{"before", "after", "comments"} {
		exe := filepath.Join(dir, "orders-"+v)
		build := exec.Command("go", "build", "-o", exe, ".")
		build.Dir = filepath.Join("..", "examples", "orders", v)
		build.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off")
		if out, err := build.CombinedOutput(); err != nil {
			t.Skipf("can't build examples/orders/%s offline: %v\n%s", v, err, out)
		}
		scrapes[v] = scrapeLive(t, exe, filepath.Join(dir, v+".prom"))
	}
	code, out, errOut := seriesdiff(t, "compare", "--fail", scrapes["before"], scrapes["comments"])
	if code != 0 || !strings.HasPrefix(out, "No changes") {
		t.Fatalf("before vs comments: code %d\n%s%s", code, out, errOut)
	}
	code, out, errOut = seriesdiff(t, "compare", "--fail", scrapes["before"], scrapes["after"])
	if code != 1 {
		t.Fatalf("before vs after: code %d\n%s%s", code, out, errOut)
	}
	// The live result matches the saved one in examples/scrapes, apart from paths.
	_, saved, _ := seriesdiff(t, "compare", "examples/scrapes/orders-before.prom", "examples/scrapes/orders-after.prom")
	if out != saved {
		t.Fatalf("live result differs from the saved scrapes\n--- live\n%s\n--- saved\n%s", out, saved)
	}
}

func scrapeLive(t *testing.T, exe, dest string) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	cmd := exec.Command(exe, "-addr", addr)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()
	base := "http://" + addr
	client := &http.Client{Timeout: 5 * time.Second}
	ready := false
	for i := 0; i < 100 && !ready; i++ {
		if resp, err := client.Get(base + "/health"); err == nil {
			resp.Body.Close()
			ready = true
		} else {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if !ready {
		t.Fatalf("%s didn't start", exe)
	}
	// The same requests as scripts/scrape-examples.sh.
	for i := 0; i < 3; i++ {
		resp, err := client.Post(base+"/orders", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	for _, p := range []string{"/orders/1001", "/orders/1002", "/orders/1003", "/orders/abc", "/pay", "/pay?fail=card_declined", "/pay?fail=insufficient_funds", "/health"} {
		resp, err := client.Get(base + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	resp, err := client.Get(base + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("scrape: %v %s", err, resp.Status)
	}
	if err := os.WriteFile(dest, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return dest
}
