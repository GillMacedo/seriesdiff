package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const examples = "../../examples"

func mustParse(t *testing.T, text string) *Scrape {
	t.Helper()
	s, err := Parse("test", []byte(text))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return s
}

func parseErr(t *testing.T, text string) string {
	t.Helper()
	_, err := Parse("test", []byte(text))
	if err == nil {
		t.Fatalf("Parse accepted %q", text)
	}
	if _, ok := err.(*InputError); !ok {
		t.Fatalf("error is %T, want *InputError", err)
	}
	return err.Error()
}

func runCLI(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := run(args, strings.NewReader(""), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestParseTextFormat(t *testing.T) {
	s := mustParse(t, `# HELP http_requests_total Requests.
# TYPE http_requests_total counter
http_requests_total{code="200",method="get"} 10
http_requests_total{code="500",method="get"} 1 1700000000000
# TYPE lat histogram
lat_bucket{le="0.1"} 1
lat_bucket{le="+Inf"} 2
lat_sum 0.3
lat_count 2
# TYPE q summary
q{quantile="0.5"} 1
q_sum 3
q_count 3
up 1
`)
	if s.Series != 10 || len(s.Families) != 4 {
		t.Fatalf("series %d families %d", s.Series, len(s.Families))
	}
	f := s.Families["http_requests_total"]
	if f.Type != "counter" || f.Series != 2 || len(f.values["code"]) != 2 {
		t.Fatalf("counter family: %+v", f)
	}
	if lat := s.Families["lat"]; lat.Type != "histogram" || lat.Series != 4 || !lat.buckets["0.1"] || lat.values["le"] != nil {
		t.Fatalf("histogram family: %+v", lat)
	}
	if q := s.Families["q"]; q.values["quantile"] != nil {
		t.Fatal("quantile should not count as a label")
	}
	if s.Families["up"].Type != "untyped" {
		t.Fatal("up should be untyped")
	}
}

func TestParseLabelEscapesAndQuotedNames(t *testing.T) {
	s := mustParse(t, `# TYPE "my.metric" gauge
{"my.metric", "service.name"="a\"b\\c\nd", x="1"} 1
m{a="x",} 2
`)
	f := s.Families["my.metric"]
	if f == nil || f.Series != 1 {
		t.Fatalf("quoted family not found: %v", s.Order)
	}
	if !f.values["service.name"]["a\"b\\c\nd"] {
		t.Fatalf("escapes not decoded: %v", f.values)
	}
	if s.Families["m"].Series != 1 {
		t.Fatal("trailing comma in labels not accepted")
	}
}

func TestParseValues(t *testing.T) {
	s := mustParse(t, "a NaN\nb +Inf\nc -Inf\nd 1.5e+06\ne -0\n")
	if s.Series != 5 {
		t.Fatalf("series %d", s.Series)
	}
}

func TestParseRefusesBadInput(t *testing.T) {
	cases := map[string]struct{ text, want string }{
		"empty":            {"", "empty"},
		"blank":            {"\n \n", "empty"},
		"no newline":       {"up 1", "doesn't end with a newline"},
		"binary":           {"up 1\n\x00\x01\n", "binary"},
		"invalid utf8":     {"up{a=\"\xff\"} 1\n", "binary"},
		"comments only":    {"# just a comment\n", "no samples"},
		"prose":            {"hello world\n", "bad value"},
		"no value":         {"up\n", "expected a metric name"},
		"bad label":        {"up{a=b} 1\n", "no quoted value"},
		"open label":       {"up{a=\"b} 1\n", "no closing quote"},
		"no brace":         {"up{a=\"b\" 1\n", "expected ','"},
		"three fields":     {"up 1 2 3\n", "expected a metric name"},
		"bad timestamp":    {"up 1 x\n", "bad timestamp"},
		"duplicate":        {"up 1\nup 2\n", "duplicate series"},
		"dup label":        {"up{a=\"1\",a=\"2\"} 1\n", "given twice"},
		"bad type":         {"# TYPE up widget\nup 1\n", "bad TYPE"},
		"second type":      {"# TYPE up gauge\nup 1\n# TYPE up gauge\n", "second TYPE"},
		"metadata at end":  {"up 1\n# HELP down Down.\n# TYPE down gauge\n", "ends after the HELP or TYPE"},
		"histogram no inf": {"# TYPE h histogram\nh_bucket{le=\"1\"} 1\nh_sum 1\nh_count 1\n", "+Inf"},
		"histogram cut":    {"# TYPE h histogram\nh_bucket{le=\"1\"} 1\nh_bucket{le=\"+Inf\"} 1\nh_sum 1\n", "no _count"},
		"summary cut":      {"# TYPE s summary\ns{quantile=\"0.5\"} 1\ns_sum 1\n", "no _count"},
		"om without eof":   {"# TYPE j counter\n# UNIT j seconds\nj_total 1\n", "# EOF"},
		"after eof":        {"up 1\n# EOF\nup 2\n", "after '# EOF'"},
		"html":             {"<html>\n</html>\n", "no metric name"},
		"curl error":       {"curl: (7) Failed to connect\n", "expected a metric name"},
		"go build error":   {"# example.com/x\n./main.go:4:2: undefined: f\n", "no metric name"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if msg := parseErr(t, c.text); !strings.Contains(msg, c.want) {
				t.Fatalf("error %q doesn't contain %q", msg, c.want)
			}
		})
	}
}

func TestNativeHistogramWithoutBucketsIsAccepted(t *testing.T) {
	// Prometheus writes native-only histograms as _sum and _count in the text format.
	s := mustParse(t, "# TYPE n histogram\nn_sum 1\nn_count 2\n")
	if s.Families["n"].Series != 2 {
		t.Fatal("native histogram not parsed")
	}
}

func TestOpenMetricsMatchesTextFormat(t *testing.T) {
	om := filepath.Join(examples, "scrapes", "jobs.om")
	text := filepath.Join(examples, "scrapes", "jobs.prom")
	code, out, errOut := runCLI("compare", "--fail", text, om)
	if code != 0 || !strings.HasPrefix(out, "No changes") {
		t.Fatalf("code %d\n%s%s", code, out, errOut)
	}
	data, _ := os.ReadFile(om)
	s, err := Parse(om, data)
	if err != nil {
		t.Fatal(err)
	}
	if !s.OpenMetrics || s.Families["jobs_processed_total"] == nil {
		t.Fatalf("OpenMetrics counter not renamed to _total: %v", s.Order)
	}
	if !s.Families["job_duration_seconds"].buckets["1"] {
		t.Fatal(`le="1.0" not normalized to "1"`)
	}
}

func TestLooksLikeID(t *testing.T) {
	ids := []string{"/orders/1001", "/users/8a3c9e1e-2b9f-4c1d-9f7a-0c1b2d3e4f50/cart", "550e8400-e29b-41d4-a716-446655440000",
		"0af7651916cd43dd", "1234567", "10.0.0.12", "10.0.0.12:8080", "someone@example.com", "/items/123?x=1"}
	for _, v := range ids {
		if !LooksLikeID(v) {
			t.Errorf("%q should look like an ID", v)
		}
	}
	notIDs := []string{"200", "404", "GET", "/orders/{id}", "/v2/orders", "/api/:id", "eth0", "go1.27.3", "card_declined", "8080", "abcdef", "deadbeef"}
	for _, v := range notIDs {
		if LooksLikeID(v) {
			t.Errorf("%q shouldn't look like an ID", v)
		}
	}
}

func TestCompareFindsEachKind(t *testing.T) {
	base := mustParse(t, `# TYPE old_name gauge
old_name 1
# TYPE kind counter
kind 1
# TYPE errs_total counter
errs_total{reason="a"} 1
errs_total{reason="b"} 1
# TYPE h histogram
h_bucket{le="1"} 1
h_bucket{le="+Inf"} 1
h_sum 1
h_count 1
# TYPE req_total counter
req_total{code="200",route="/a"} 1
req_total{code="500",route="/a"} 1
`)
	head := mustParse(t, `# TYPE kind gauge
kind 1
# TYPE errs_total counter
errs_total 1
# TYPE h histogram
h_bucket{le="2"} 1
h_bucket{le="+Inf"} 1
h_sum 1
h_count 1
# TYPE new_name gauge
new_name 1
# TYPE old_name_v2 gauge
old_name_v2 1
# TYPE req_total counter
req_total{code="200",route="/a"} 1
req_total{code="200",route="/users/123"} 1
req_total{code="200",route="/users/456"} 1
`)
	r := Compare(base, head, Options{MaxGrowth: 50})
	if len(r.Removed) != 1 || r.Removed[0].Name != "old_name" || r.Removed[0].RenamedTo != "old_name_v2" {
		t.Errorf("removed: %+v", r.Removed)
	}
	if len(r.TypeChanges) != 1 || r.TypeChanges[0].Base != "counter" || r.TypeChanges[0].Head != "gauge" {
		t.Errorf("type changes: %+v", r.TypeChanges)
	}
	if len(r.LabelsGone) != 1 || r.LabelsGone[0].Label != "reason" || r.LabelsGone[0].Values != 2 {
		t.Errorf("labels gone: %+v", r.LabelsGone)
	}
	if len(r.Buckets) != 1 || strings.Join(r.Buckets[0].Removed, ",") != "1" || strings.Join(r.Buckets[0].Added, ",") != "2" {
		t.Errorf("buckets: %+v", r.Buckets)
	}
	if len(r.IDLabels) != 1 || r.IDLabels[0].Label != "route" {
		t.Errorf("ID labels: %+v", r.IDLabels)
	}
	// req_total grew, but lost code="500", so it's a lost series.
	if len(r.Grown) != 0 || len(r.Lost) != 1 || r.Lost[0].Metric != "req_total" || r.Lost[0].Gone["code"][0] != "500" {
		t.Errorf("grown: %+v lost: %+v", r.Grown, r.Lost)
	}
	if len(r.Added) != 2 {
		t.Errorf("added: %+v", r.Added)
	}
	if r.Problems() == 0 {
		t.Error("no problems counted")
	}
}

func TestGrowthLimit(t *testing.T) {
	base := mustParse(t, "a{x=\"1\"} 1\na{x=\"2\"} 1\n")
	head := mustParse(t, "a{x=\"1\"} 1\na{x=\"2\"} 1\na{x=\"3\"} 1\n")
	if r := Compare(base, head, Options{MaxGrowth: 50}); r.Problems() != 0 || len(r.Grown) != 1 || r.Grown[0].OverLimit {
		t.Fatalf("50%% growth shouldn't be over a 50%% limit: %+v", r.Grown)
	}
	if r := Compare(base, head, Options{MaxGrowth: 40}); r.Problems() != 1 || !r.Grown[0].OverLimit {
		t.Fatalf("50%% growth should be over a 40%% limit: %+v", r.Grown)
	}
}

func TestInfoMetricValueChangesAreQuiet(t *testing.T) {
	base := mustParse(t, "go_info{version=\"go1.22.5\"} 1\nother{v=\"a\"} 1\n")
	head := mustParse(t, "go_info{version=\"go1.23.7\"} 1\nother{v=\"b\"} 1\n")
	r := Compare(base, head, Options{MaxGrowth: 50})
	if len(r.Lost) != 1 || r.Lost[0].Metric != "other" {
		t.Fatalf("want only other reported, got %+v", r.Lost)
	}
}

func TestIdenticalIsQuiet(t *testing.T) {
	text := "# TYPE a counter\na_total{x=\"1\"} 1\n"
	r := Compare(mustParse(t, text), mustParse(t, text), Options{MaxGrowth: 50})
	var buf bytes.Buffer
	r.WriteText(&buf)
	if r.Problems() != 0 || !strings.HasPrefix(buf.String(), "No changes") {
		t.Fatalf("not quiet: %s", buf.String())
	}
}

func TestEndsEarly(t *testing.T) {
	full := mustParse(t, "a 1\nb 1\nc 1\nd 1\n")
	if _, _, early := mustParse(t, "a 1\nb 1\n").EndsEarly(full); !early {
		t.Error("prefix not caught")
	}
	// Cut right after a series the other side doesn't have.
	if _, n, early := mustParse(t, "a 1\nb 1\nb2 1\n").EndsEarly(full); !early || n != 2 {
		t.Errorf("cut after a removed series not caught (n=%d)", n)
	}
	if _, _, early := full.EndsEarly(full); early {
		t.Error("identical flagged")
	}
	// The other side has more, but also has something this side has after it.
	if _, _, early := mustParse(t, "a 1\nb 1\nd 1\n").EndsEarly(full); early {
		t.Error("a change in the middle flagged")
	}
	if _, _, early := mustParse(t, "x 1\n").EndsEarly(full); early {
		t.Error("unrelated scrape flagged")
	}
}

func TestRefs(t *testing.T) {
	idx, err := BuildRefs([]string{filepath.Join(examples, "monitoring")})
	if err != nil {
		t.Fatal(err)
	}
	if idx.Files != 2 {
		t.Fatalf("files %d", idx.Files)
	}
	refs := idx.find([]string{"order_queue_depth"}, "")
	if len(refs) != 2 {
		t.Fatalf("refs %+v", refs)
	}
	if got := idx.find([]string{"payment_errors_total"}, "reason"); len(got) != 2 {
		t.Fatalf("label refs %+v", got)
	}
	if got := idx.findValue([]string{"http_request_duration_seconds_bucket"}, "route", "/orders/{id}"); len(got) != 1 || got[0].Line != 8 {
		t.Fatalf("value refs %+v", got)
	}
	// A name inside a longer identifier doesn't count.
	if got := idx.find([]string{"queue_depth"}, ""); len(got) != 0 {
		t.Fatalf("partial match: %+v", got)
	}
}

func TestRunExitCodes(t *testing.T) {
	before := filepath.Join(examples, "scrapes", "orders-before.prom")
	after := filepath.Join(examples, "scrapes", "orders-after.prom")
	comments := filepath.Join(examples, "scrapes", "orders-comments.prom")
	cases := []struct {
		args []string
		code int
	}{
		{[]string{"compare", before, comments}, 0},
		{[]string{"compare", "--fail", before, comments}, 0},
		{[]string{"compare", before, after}, 0},
		{[]string{"compare", "--fail", before, after}, 1},
		{[]string{"compare", before, after, "--fail"}, 1},
		{[]string{"compare", "--fail", "--max-growth", "-1", before, after}, 2},
		{[]string{"compare", before}, 2},
		{[]string{"compare", "-", "-"}, 2},
		{[]string{"compare", before, filepath.Join(examples, "nope.prom")}, 2},
		{[]string{"compare", "--refs", filepath.Join(examples, "nope"), before, after}, 2},
		{[]string{"frobnicate"}, 2},
		{[]string{}, 2},
		{[]string{"version"}, 0},
	}
	for _, c := range cases {
		if code, out, errOut := runCLI(c.args...); code != c.code {
			t.Errorf("%v: code %d, want %d\n%s%s", c.args, code, c.code, out, errOut)
		}
	}
}

func TestJSON(t *testing.T) {
	code, out, _ := runCLI("compare", "--json", "--refs", filepath.Join(examples, "monitoring"),
		filepath.Join(examples, "scrapes", "orders-before.prom"), filepath.Join(examples, "scrapes", "orders-after.prom"))
	if code != 0 {
		t.Fatalf("code %d", code)
	}
	var r Report
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Removed) != 1 || len(r.Removed[0].Refs) != 2 || r.RefsSearched != 2 {
		t.Fatalf("json: %+v", r.Removed)
	}
}

// realScrapes lists every real scrape in examples/scrapes.
func realScrapes(t *testing.T) []string {
	files, _ := filepath.Glob(filepath.Join(examples, "scrapes", "*.prom"))
	more, _ := filepath.Glob(filepath.Join(examples, "scrapes", "real", "*.prom"))
	om, _ := filepath.Glob(filepath.Join(examples, "scrapes", "*.om"))
	files = append(append(files, more...), om...)
	if len(files) < 10 {
		t.Fatalf("only %d scrapes found", len(files))
	}
	return files
}

func TestEveryRealScrapeParses(t *testing.T) {
	for _, f := range realScrapes(t) {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(f, data); err != nil {
			t.Errorf("%v", err)
		}
	}
}

// TestCutScrapesAreNeverTrusted cuts real scrapes at every line break and at
// points inside lines, on each side, against the same file and against a
// really different one. None may exit 0, with or without --fail, and each
// must say the input can't be trusted.
func TestCutScrapesAreNeverTrusted(t *testing.T) {
	pairs := [][2]string{
		{"orders-before.prom", "orders-comments.prom"},
		{"orders-before.prom", "orders-after.prom"},
		{"jobs.prom", "jobs.om"},
		{"real/node_exporter-1.10.1.prom", "real/node_exporter-1.10.2.prom"},
		{"real/node_exporter-1.8.2.prom", "real/node_exporter-1.9.1.prom"},
		{"real/prometheus-2.55.1.prom", "real/prometheus-3.0.1.prom"},
	}
	for _, pair := range pairs {
		pair := pair
		t.Run(pair[0]+"|"+pair[1], func(t *testing.T) {
			t.Parallel()
			cut := filepath.Join(t.TempDir(), "cut.prom")
			checked := 0
			paths := [2]string{filepath.Join(examples, "scrapes", pair[0]), filepath.Join(examples, "scrapes", pair[1])}
			for side := 0; side < 2; side++ {
				data, err := os.ReadFile(paths[side])
				if err != nil {
					t.Fatal(err)
				}
				var offsets []int
				for i, b := range data[:len(data)-1] {
					if b == '\n' {
						offsets = append(offsets, i+1)
					}
				}
				for i := 1; i < len(data)-1; i += len(data)/50 + 1 {
					offsets = append(offsets, i)
				}
				if testing.Short() {
					offsets = offsets[len(offsets)-20:]
				}
				for _, off := range offsets {
					if err := os.WriteFile(cut, data[:off], 0o600); err != nil {
						t.Fatal(err)
					}
					args := paths
					args[side] = cut
					for _, fail := range []bool{false, true} {
						cmd := []string{"compare", args[0], args[1]}
						if fail {
							cmd = []string{"compare", "--fail", args[0], args[1]}
						}
						code, out, errOut := runCLI(cmd...)
						checked++
						trusted := !strings.Contains(errOut, "not a usable scrape") && !strings.Contains(out, "may be cut off")
						if code == 0 || strings.HasPrefix(out, "No changes") || trusted {
							t.Fatalf("%s cut at byte %d (side %d, fail %v): code %d\n%s%s", paths[side], off, side, fail, code, out, errOut)
						}
					}
				}
			}
			t.Logf("checked %d cut inputs", checked)
		})
	}
}

func TestBadInputsOnEachSide(t *testing.T) {
	bad, _ := filepath.Glob("../../tests/testdata/bad/*")
	if len(bad) < 10 {
		t.Fatalf("only %d bad inputs", len(bad))
	}
	good := filepath.Join(examples, "scrapes", "orders-before.prom")
	for _, b := range bad {
		for side := 0; side < 2; side++ {
			args := []string{good, good}
			args[side] = b
			for _, mode := range [][]string{{"compare"}, {"compare", "--fail"}} {
				code, out, errOut := runCLI(append(mode, args...)...)
				if code != 2 || out != "" || !strings.Contains(errOut, "not a usable scrape") {
					t.Errorf("%s on side %d: code %d\n%s%s", b, side, code, out, errOut)
				}
			}
		}
	}
}

func TestTrustEnds(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.prom")
	head := filepath.Join(dir, "head.prom")
	os.WriteFile(base, []byte("a 1\nb 1\n"), 0o600)
	os.WriteFile(head, []byte("a 1\nb 1\nzz_new_total 1\n"), 0o600)
	code, out, _ := runCLI("compare", base, head)
	if code != 2 || !strings.HasPrefix(out, "WARNING: base may be cut off") || !strings.Contains(out, "zz_new_total") {
		t.Fatalf("code %d\n%s", code, out)
	}
	if code, out, _ = runCLI("compare", "--fail", "--trust-ends", base, head); code != 0 || strings.Contains(out, "WARNING") {
		t.Fatalf("--trust-ends: code %d\n%s", code, out)
	}
	if code, out, _ = runCLI("compare", "--fail", head, base); code != 2 || !strings.HasPrefix(out, "WARNING: head may be cut off") {
		t.Fatalf("head cut: code %d\n%s", code, out)
	}
	if code, _, _ = runCLI("compare", "--fail", "--trust-ends", head, base); code != 1 {
		t.Fatalf("removed metric with --trust-ends: code %d", code)
	}
}

func TestStdin(t *testing.T) {
	before := filepath.Join(examples, "scrapes", "orders-before.prom")
	data, _ := os.ReadFile(before)
	var out, errOut bytes.Buffer
	if code := run([]string{"compare", before, "-"}, bytes.NewReader(data), &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "No changes") {
		t.Fatalf("code %d\n%s%s", code, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := run([]string{"compare", before, "-"}, strings.NewReader(""), &out, &errOut); code != 2 {
		t.Fatalf("empty stdin: code %d", code)
	}
}
