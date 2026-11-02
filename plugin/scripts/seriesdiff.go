// Command seriesdiff compares two Prometheus /metrics scrapes, such as one
// taken from a service built from the base branch and one from a pull
// request, and reports what would break dashboards and alerts or grow
// cardinality: removed metrics, type changes, removed labels, changed
// histogram buckets, label values that look like IDs, and series growth.
//
// It reads the Prometheus text exposition format and OpenMetrics text. It
// refuses input it can't trust, such as an empty file, an error page, or a
// scrape that was cut off, instead of reporting no changes.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const version = "0.1.0"

// maxLine is the longest line seriesdiff reads.
const maxLine = 4 * 1024 * 1024

// Sample is one line of a scrape, which is one time series in Prometheus.
type Sample struct {
	Name   string
	Labels map[string]string
	Value  float64
	Line   int
}

// Family is one metric family: the samples that share a TYPE.
type Family struct {
	Name     string                     `json:"name"`
	Type     string                     `json:"type"`
	Unit     string                     `json:"unit,omitempty"`
	Series   int                        `json:"series"`
	Line     int                        `json:"line"`
	samples  []Sample                   // in file order
	keys     map[string]bool            // sample name + labels, to find duplicates
	values   map[string]map[string]bool // label name -> values, without le and quantile
	buckets  map[string]bool            // le values of histogram buckets
	declared bool
	typeLine int
}

// Scrape is one parsed /metrics file.
type Scrape struct {
	Path        string
	Families    map[string]*Family
	Order       []string
	Series      int
	OpenMetrics bool
	keys        []string // series in file order, to spot a file cut off at a line break
}

// InputError means a file can't be trusted, so no comparison is made.
type InputError struct {
	Path string
	Msg  string
}

func (e *InputError) Error() string {
	return fmt.Sprintf("%s: %s", e.Path, e.Msg)
}

var validTypes = map[string]bool{
	"counter": true, "gauge": true, "histogram": true, "gaugehistogram": true,
	"summary": true, "untyped": true, "unknown": true, "info": true, "stateset": true,
}

// suffixes lists the sample name suffixes each type allows after the family name.
var suffixes = map[string][]string{
	"counter":        {"_total", "_created"},
	"histogram":      {"_bucket", "_sum", "_count", "_created"},
	"gaugehistogram": {"_bucket", "_gsum", "_gcount"},
	"summary":        {"_sum", "_count", "_created"},
	"info":           {"_info"},
}

// Parse reads one scrape. It returns an *InputError for anything that
// suggests the file isn't a complete, valid scrape.
func Parse(path string, data []byte) (*Scrape, error) {
	s := &Scrape{Path: path, Families: map[string]*Family{}}
	bad := func(line int, format string, args ...any) error {
		msg := fmt.Sprintf(format, args...)
		if line > 0 {
			msg = fmt.Sprintf("line %d: %s", line, msg)
		}
		return &InputError{Path: path, Msg: msg}
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, bad(0, "the file is empty, so it isn't a scrape. Check that the scrape command succeeded")
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return nil, bad(0, "the file is binary, not the Prometheus text format. If it came from a scrape, request plain text with no compression")
	}
	if data[len(data)-1] != '\n' {
		return nil, bad(0, "the file doesn't end with a newline, so it was cut off or isn't a scrape")
	}
	lines := strings.Split(string(data[:len(data)-1]), "\n")
	sawEOF := 0
	lastSample := 0
	var current *Family
	for i, raw := range lines {
		n := i + 1
		line := strings.TrimSuffix(raw, "\r")
		if len(line) > maxLine {
			return nil, bad(n, "line is longer than %d bytes", maxLine)
		}
		if sawEOF > 0 {
			if strings.TrimSpace(line) != "" {
				return nil, bad(n, "content after '# EOF' on line %d. Two scrapes may have been joined into one file", sawEOF)
			}
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[1] == "EOF" {
				sawEOF = n
				s.OpenMetrics = true
				continue
			}
			if len(fields) < 3 || (fields[1] != "HELP" && fields[1] != "TYPE" && fields[1] != "UNIT") {
				continue // other comments are allowed
			}
			name, err := unquoteName(fields[2])
			if err != nil {
				return nil, bad(n, "%v", err)
			}
			f := s.Families[name]
			if f == nil {
				f = newFamily(name, n)
				s.Families[name] = f
				s.Order = append(s.Order, name)
			}
			switch fields[1] {
			case "TYPE":
				if len(fields) != 4 || !validTypes[fields[3]] {
					return nil, bad(n, "bad TYPE line: %q", line)
				}
				if f.declared {
					return nil, bad(n, "second TYPE line for %s (first on line %d). Two scrapes may have been joined into one file", name, f.typeLine)
				}
				if len(f.samples) > 0 {
					return nil, bad(n, "TYPE line for %s comes after its samples", name)
				}
				f.Type, f.declared, f.typeLine = fields[3], true, n
				if f.Type == "unknown" || f.Type == "info" || f.Type == "stateset" || f.Type == "gaugehistogram" {
					s.OpenMetrics = true
				}
			case "UNIT":
				if len(fields) == 4 {
					f.Unit = fields[3]
				}
				s.OpenMetrics = true
			}
			current = f
			continue
		}
		smp, exemplar, err := parseSample(line, n)
		if err != nil {
			return nil, bad(n, "%v. This doesn't look like a Prometheus scrape", err)
		}
		if exemplar {
			s.OpenMetrics = true
		}
		f := s.familyFor(smp.Name, current)
		key := seriesKey(smp.Name, smp.Labels)
		if f.keys[key] {
			return nil, bad(n, "duplicate series %s. Two scrapes may have been joined into one file", key)
		}
		f.keys[key] = true
		lastSample = n
		current = f
		if smp.Name == f.Name+"_created" && f.Name != "" {
			// OpenMetrics writes a _created line for each counter, histogram
			// and summary. It's a timestamp, not a series a query would use,
			// and the Prometheus text format leaves it out, so it isn't counted.
			continue
		}
		f.samples = append(f.samples, smp)
		f.Series++
		s.Series++
		s.keys = append(s.keys, key)
		for k, v := range smp.Labels {
			if k == "le" && smp.Name == f.Name+"_bucket" {
				f.buckets[v] = true
				continue
			}
			if k == "quantile" && f.Type == "summary" && smp.Name == f.Name {
				continue
			}
			if f.values[k] == nil {
				f.values[k] = map[string]bool{}
			}
			f.values[k][v] = true
		}
		current = f
	}
	if s.Series == 0 {
		return nil, bad(0, "the file has no samples, so it isn't a usable scrape")
	}
	if s.OpenMetrics && sawEOF == 0 {
		return nil, bad(0, "OpenMetrics output must end with '# EOF', and this file doesn't, so it was probably cut off")
	}
	for _, name := range s.Order {
		f := s.Families[name]
		if f.Series == 0 && f.Line > lastSample {
			return nil, bad(f.Line, "the file ends after the HELP or TYPE line of %s with no samples, so it was probably cut off", name)
		}
		if err := f.checkComplete(); err != nil {
			return nil, bad(0, "%v", err)
		}
	}
	// Families declared with no samples carry nothing to compare.
	// OpenMetrics names a counter family without _total, while the
	// Prometheus text format includes it. Use the name with _total, which is
	// the name queries use, so both formats compare equal.
	kept := s.Order[:0]
	for _, name := range s.Order {
		f := s.Families[name]
		if f.Series == 0 {
			delete(s.Families, name)
			continue
		}
		if f.Type == "counter" && !strings.HasSuffix(name, "_total") && s.Families[name+"_total"] == nil && f.samples[0].Name == name+"_total" {
			delete(s.Families, name)
			f.Name = name + "_total"
			s.Families[f.Name] = f
			name = f.Name
		}
		kept = append(kept, name)
	}
	s.Order = kept
	return s, nil
}

func newFamily(name string, line int) *Family {
	return &Family{Name: name, Type: "untyped", Line: line, keys: map[string]bool{},
		values: map[string]map[string]bool{}, buckets: map[string]bool{}}
}

// familyFor finds the family a sample belongs to: the family being read if
// the name fits its type, else a declared family whose name plus a type
// suffix gives the sample name, else a new untyped family.
func (s *Scrape) familyFor(sample string, current *Family) *Family {
	fits := func(f *Family) bool {
		if f == nil {
			return false
		}
		if sample == f.Name {
			return f.Type != "histogram" && f.Type != "gaugehistogram" && f.Type != "info"
		}
		for _, suf := range suffixes[f.Type] {
			if sample == f.Name+suf {
				return true
			}
		}
		return false
	}
	if fits(current) {
		return current
	}
	if f := s.Families[sample]; fits(f) {
		return f
	}
	for _, suf := range []string{"_total", "_created", "_bucket", "_sum", "_count", "_gsum", "_gcount", "_info"} {
		if base, ok := strings.CutSuffix(sample, suf); ok {
			if f := s.Families[base]; fits(f) {
				return f
			}
		}
	}
	if f := s.Families[sample]; f != nil && !f.declared {
		return f
	}
	if f := s.Families[sample]; f != nil {
		// A declared family whose type doesn't allow this name. Keep it
		// separate, under a name that can't clash.
		sample += " (untyped)"
		if g := s.Families[sample]; g != nil {
			return g
		}
	}
	f := newFamily(sample, 0)
	s.Families[sample] = f
	s.Order = append(s.Order, sample)
	return f
}

// checkComplete finds histograms and summaries missing the lines a client
// always writes, which happens when a scrape is cut off at a line break.
// A histogram with _sum and _count but no buckets is allowed: that's how a
// native histogram looks in the text format.
func (f *Family) checkComplete() error {
	if f.Series == 0 {
		return nil
	}
	groups := map[string]map[string]bool{}
	for _, smp := range f.samples {
		labels := map[string]string{}
		for k, v := range smp.Labels {
			if k == "le" || (k == "quantile" && f.Type == "summary") {
				continue
			}
			labels[k] = v
		}
		g := seriesKey("", labels)
		if groups[g] == nil {
			groups[g] = map[string]bool{}
		}
		part := strings.TrimPrefix(smp.Name, f.Name)
		if part == "_bucket" && smp.Labels["le"] == "+Inf" {
			part = "+Inf"
		}
		if part == "" && f.Type == "summary" {
			part = "quantile"
		}
		groups[g][part] = true
	}
	for g, parts := range groups {
		where := f.Name + g
		switch f.Type {
		case "histogram":
			if parts["_bucket"] && !parts["+Inf"] {
				return fmt.Errorf("histogram %s has buckets but no le=\"+Inf\" bucket, so the scrape was probably cut off", where)
			}
			if parts["_bucket"] && !parts["_count"] {
				return fmt.Errorf("histogram %s has buckets but no _count, so the scrape was probably cut off", where)
			}
		case "summary":
			if (parts["quantile"] || parts["_sum"]) && !parts["_count"] {
				return fmt.Errorf("summary %s has no _count, so the scrape was probably cut off", where)
			}
		}
	}
	return nil
}

var nameRe = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)

func unquoteName(s string) (string, error) {
	if strings.HasPrefix(s, `"`) {
		u, err := strconv.Unquote(s)
		if err != nil || u == "" {
			return "", fmt.Errorf("bad quoted metric name %s", s)
		}
		return u, nil
	}
	if !nameRe.MatchString(s) {
		return "", fmt.Errorf("bad metric name %q", s)
	}
	return s, nil
}

// parseSample reads `name{labels} value [timestamp] [# exemplar]`. It also
// accepts the quoted form `{"name", label="v"} value` used for UTF-8 names.
func parseSample(line string, n int) (Sample, bool, error) {
	smp := Sample{Labels: map[string]string{}, Line: n}
	i := 0
	for i < len(line) && isNameChar(line[i], i == 0) {
		i++
	}
	smp.Name = line[:i]
	rest := line[i:]
	if strings.HasPrefix(rest, "{") {
		end, err := parseLabels(rest, &smp)
		if err != nil {
			return smp, false, err
		}
		rest = rest[end:]
	}
	if smp.Name == "" {
		return smp, false, fmt.Errorf("no metric name in %q", short(line))
	}
	exemplar := false
	if j := strings.Index(rest, " # "); j >= 0 {
		exemplar = true
		rest = rest[:j]
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 || len(fields) > 2 || !strings.HasPrefix(rest, " ") && !strings.HasPrefix(rest, "\t") {
		return smp, false, fmt.Errorf("expected a metric name, labels and a value, got %q", short(line))
	}
	v, err := parseValue(fields[0])
	if err != nil {
		return smp, false, fmt.Errorf("bad value %q", short(fields[0]))
	}
	smp.Value = v
	if len(fields) == 2 {
		if _, err := strconv.ParseFloat(fields[1], 64); err != nil {
			return smp, false, fmt.Errorf("bad timestamp %q", short(fields[1]))
		}
	}
	return smp, exemplar, nil
}

func isNameChar(c byte, first bool) bool {
	if c == '_' || c == ':' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
		return true
	}
	return !first && c >= '0' && c <= '9'
}

func parseValue(s string) (float64, error) {
	switch s {
	case "+Inf", "Inf":
		return math.Inf(1), nil
	case "-Inf":
		return math.Inf(-1), nil
	case "NaN":
		return math.NaN(), nil
	}
	return strconv.ParseFloat(s, 64)
}

// parseLabels reads `{a="b",c="d"}` from the start of s and returns the
// index after the closing brace.
func parseLabels(s string, smp *Sample) (int, error) {
	i := 1
	first := true
	for {
		for i < len(s) && s[i] == ' ' {
			i++
		}
		if i >= len(s) {
			return 0, errors.New("labels have no closing brace")
		}
		if s[i] == '}' {
			return i + 1, nil
		}
		if !first {
			if s[i] != ',' {
				return 0, fmt.Errorf("expected ',' between labels at %q", short(s[i:]))
			}
			i++
			for i < len(s) && s[i] == ' ' {
				i++
			}
			if i < len(s) && s[i] == '}' {
				return i + 1, nil
			}
		}
		first = false
		var name string
		if i < len(s) && s[i] == '"' {
			str, j, err := readQuoted(s, i)
			if err != nil {
				return 0, err
			}
			i = j
			for i < len(s) && s[i] == ' ' {
				i++
			}
			if i < len(s) && (s[i] == ',' || s[i] == '}') {
				// A quoted metric name inside the braces.
				if smp.Name != "" {
					return 0, errors.New("metric name given twice")
				}
				smp.Name = str
				continue
			}
			name = str
		} else {
			j := i
			for j < len(s) && isNameChar(s[j], j == i) {
				j++
			}
			if j == i {
				return 0, fmt.Errorf("bad label name at %q", short(s[i:]))
			}
			name = s[i:j]
			i = j
		}
		for i < len(s) && s[i] == ' ' {
			i++
		}
		if i >= len(s) || s[i] != '=' {
			return 0, fmt.Errorf("expected '=' after label %s", name)
		}
		i++
		for i < len(s) && s[i] == ' ' {
			i++
		}
		if i >= len(s) || s[i] != '"' {
			return 0, fmt.Errorf("label %s has no quoted value", name)
		}
		val, j, err := readQuoted(s, i)
		if err != nil {
			return 0, err
		}
		i = j
		if _, dup := smp.Labels[name]; dup {
			return 0, fmt.Errorf("label %s given twice", name)
		}
		if name == "le" || name == "quantile" {
			// OpenMetrics writes le="1.0" where the text format writes le="1".
			if v, err := parseValue(val); err == nil {
				val = formatFloat(v)
			}
		}
		smp.Labels[name] = val
	}
}

// readQuoted reads a double-quoted string with \\, \" and \n escapes.
func readQuoted(s string, i int) (string, int, error) {
	var b strings.Builder
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			if j+1 >= len(s) {
				return "", 0, errors.New("unfinished escape in label value")
			}
			j++
			switch s[j] {
			case 'n':
				b.WriteByte('\n')
			case '\\', '"':
				b.WriteByte(s[j])
			default:
				b.WriteByte('\\')
				b.WriteByte(s[j])
			}
		case '"':
			return b.String(), j + 1, nil
		default:
			b.WriteByte(s[j])
		}
	}
	return "", 0, errors.New("label value has no closing quote")
}

func formatFloat(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

func short(s string) string {
	if len(s) > 60 {
		return s[:57] + "..."
	}
	return s
}

// seriesKey is name{a="1",b="2"} with labels sorted.
func seriesKey(name string, labels map[string]string) string {
	if len(labels) == 0 {
		return name
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(name)
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(strconv.Quote(labels[k]))
	}
	b.WriteByte('}')
	return b.String()
}

// EndsEarly reports whether s stops where other continues: the last
// series of s that other also has is at some position in other, and none
// of the series other has after it are in s. That's what a scrape cut off at a line break looks
// like next to a complete one, because exporters write metrics in a fixed
// order. It returns the last series of s and how many series other has after it.
func (s *Scrape) EndsEarly(other *Scrape) (string, int, bool) {
	if len(s.keys) == 0 {
		return "", 0, false
	}
	pos := map[string]int{}
	for i, k := range other.keys {
		pos[k] = i
	}
	// Start from the last series of s that other also has. When s was cut
	// right after a series that other doesn't have, look further back.
	p := -1
	for i := len(s.keys) - 1; i >= 0 && p < 0; i-- {
		if j, ok := pos[s.keys[i]]; ok {
			p = j
		}
	}
	if p < 0 {
		return "", 0, false
	}
	last := s.keys[len(s.keys)-1]
	after := other.keys[p+1:]
	if len(after) == 0 {
		return "", 0, false
	}
	mine := make(map[string]bool, len(s.keys))
	for _, k := range s.keys {
		mine[k] = true
	}
	for _, k := range after {
		if mine[k] {
			return "", 0, false
		}
	}
	return last, len(after), true
}

// ---- Comparison ----

// Ref is a place in a rules, dashboard or other text file that names a metric.
type Ref struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

// Removed is a metric family in base that's missing in head.
type Removed struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Series    int    `json:"series"`
	RenamedTo string `json:"probablyRenamedTo,omitempty"`
	Refs      []Ref  `json:"refs,omitempty"`
}

// Added is a metric family in head that's missing in base.
type Added struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Series int    `json:"series"`
}

// TypeChange is a family whose TYPE changed.
type TypeChange struct {
	Name string `json:"name"`
	Base string `json:"base"`
	Head string `json:"head"`
	Refs []Ref  `json:"refs,omitempty"`
}

// LabelChange is a label that was removed from or added to a family.
type LabelChange struct {
	Metric string `json:"metric"`
	Label  string `json:"label"`
	Values int    `json:"values"`
	Refs   []Ref  `json:"refs,omitempty"`
}

// BucketChange is a histogram whose bucket boundaries changed.
type BucketChange struct {
	Metric  string   `json:"metric"`
	Removed []string `json:"removed,omitempty"`
	Added   []string `json:"added,omitempty"`
	Refs    []Ref    `json:"refs,omitempty"`
}

// IDLabel is a label whose values look like IDs, which grow without bound.
type IDLabel struct {
	Metric   string   `json:"metric"`
	Label    string   `json:"label"`
	Values   int      `json:"values"`
	Examples []string `json:"examples"`
}

// SeriesChange is a family present on both sides whose series count changed.
type SeriesChange struct {
	Metric     string              `json:"metric"`
	Base       int                 `json:"base"`
	Head       int                 `json:"head"`
	Driver     string              `json:"label,omitempty"`
	BaseValues int                 `json:"baseValues,omitempty"`
	HeadValues int                 `json:"headValues,omitempty"`
	Gone       map[string][]string `json:"goneValues,omitempty"`
	GoneRefs   []Ref               `json:"goneValueRefs,omitempty"`
	OverLimit  bool                `json:"overLimit,omitempty"`
}

// Report is the result of comparing two scrapes.
type Report struct {
	Base         string         `json:"base"`
	Head         string         `json:"head"`
	BaseSeries   int            `json:"baseSeries"`
	HeadSeries   int            `json:"headSeries"`
	BaseMetrics  int            `json:"baseMetrics"`
	HeadMetrics  int            `json:"headMetrics"`
	Removed      []Removed      `json:"removedMetrics"`
	TypeChanges  []TypeChange   `json:"typeChanges"`
	LabelsGone   []LabelChange  `json:"removedLabels"`
	Buckets      []BucketChange `json:"bucketChanges"`
	IDLabels     []IDLabel      `json:"idLikeLabels"`
	Grown        []SeriesChange `json:"moreSeries"`
	Lost         []SeriesChange `json:"lostSeries"`
	Added        []Added        `json:"newMetrics"`
	LabelsAdded  []LabelChange  `json:"addedLabels"`
	MaxGrowth    float64        `json:"maxGrowthPercent"`
	RefsSearched int            `json:"refFilesSearched,omitempty"`
	Warnings     []string       `json:"warnings,omitempty"`
}

// Options control the comparison.
type Options struct {
	MaxGrowth float64 // percent growth in a family's series that counts as a problem
	Refs      *RefIndex
}

// Compare compares two scrapes.
func Compare(base, head *Scrape, opt Options) *Report {
	r := &Report{Base: base.Path, Head: head.Path, BaseSeries: base.Series, HeadSeries: head.Series,
		BaseMetrics: len(base.Families), HeadMetrics: len(head.Families), MaxGrowth: opt.MaxGrowth,
		Removed: []Removed{}, TypeChanges: []TypeChange{}, LabelsGone: []LabelChange{}, Buckets: []BucketChange{},
		IDLabels: []IDLabel{}, Grown: []SeriesChange{}, Lost: []SeriesChange{}, Added: []Added{}, LabelsAdded: []LabelChange{}}
	if opt.Refs != nil {
		r.RefsSearched = opt.Refs.Files
	}
	var removed []*Family
	var added []*Family
	for _, name := range sortedNames(base) {
		if head.Families[name] == nil {
			removed = append(removed, base.Families[name])
		}
	}
	for _, name := range sortedNames(head) {
		if base.Families[name] == nil {
			added = append(added, head.Families[name])
		}
	}
	for _, f := range removed {
		rm := Removed{Name: f.Name, Type: f.Type, Series: f.Series, RenamedTo: guessRename(f, added)}
		rm.Refs = opt.Refs.find(sampleNames(f), "")
		r.Removed = append(r.Removed, rm)
	}
	for _, f := range added {
		r.Added = append(r.Added, Added{Name: f.Name, Type: f.Type, Series: f.Series})
		for _, label := range sortedKeys(f.values) {
			if id := idLabel(f, label, nil); id != nil {
				r.IDLabels = append(r.IDLabels, *id)
			}
		}
	}
	for _, name := range sortedNames(base) {
		b, h := base.Families[name], head.Families[name]
		if h == nil {
			continue
		}
		if b.Type != h.Type {
			r.TypeChanges = append(r.TypeChanges, TypeChange{Name: name, Base: b.Type, Head: h.Type,
				Refs: opt.Refs.find(sampleNames(b), "")})
		}
		for _, label := range sortedKeys(b.values) {
			if h.values[label] == nil {
				r.LabelsGone = append(r.LabelsGone, LabelChange{Metric: name, Label: label, Values: len(b.values[label]),
					Refs: opt.Refs.find(sampleNames(b), label)})
			}
		}
		for _, label := range sortedKeys(h.values) {
			if b.values[label] == nil {
				r.LabelsAdded = append(r.LabelsAdded, LabelChange{Metric: name, Label: label, Values: len(h.values[label])})
			}
			if id := idLabel(h, label, b.values[label]); id != nil {
				r.IDLabels = append(r.IDLabels, *id)
			}
		}
		if b.Type == h.Type && (b.Type == "histogram" || b.Type == "gaugehistogram") && len(b.buckets) > 0 && len(h.buckets) > 0 {
			gone, added := setDiff(b.buckets, h.buckets), setDiff(h.buckets, b.buckets)
			if len(gone) > 0 || len(added) > 0 {
				r.Buckets = append(r.Buckets, BucketChange{Metric: name, Removed: sortLe(gone), Added: sortLe(added),
					Refs: opt.Refs.find([]string{name + "_bucket"}, "")})
			}
		}
		if h.Series == b.Series && (isInfo(h) || !hasGoneValues(b, h)) {
			// Same count and no lost values. Info metrics such as
			// go_info{version} change values with every build, so a
			// replaced value there isn't reported.
			continue
		}
		c := SeriesChange{Metric: name, Base: b.Series, Head: h.Series, Gone: goneValues(b, h)}
		c.Driver, c.BaseValues, c.HeadValues = driver(b, h)
		c.OverLimit = h.Series > b.Series && float64(h.Series-b.Series) > float64(b.Series)*opt.MaxGrowth/100
		labelRemoved := false
		for label := range b.values {
			if h.values[label] == nil {
				labelRemoved = true
			}
		}
		if h.Series < b.Series && c.Gone == nil && labelRemoved {
			// Fewer series because a label was removed, which is already reported.
			continue
		}
		if h.Series < b.Series || c.Gone != nil {
			r.Lost = append(r.Lost, c)
		} else {
			r.Grown = append(r.Grown, c)
		}
	}
	for _, list := range [][]SeriesChange{r.Grown, r.Lost} {
		for i := range list {
			c := &list[i]
			if opt.Refs == nil || c.Gone == nil {
				continue
			}
			seen := map[Ref]bool{}
			for _, label := range sortedKeys(c.Gone) {
				for _, v := range c.Gone[label] {
					for _, ref := range opt.Refs.findValue(sampleNames(base.Families[c.Metric]), label, v) {
						if !seen[ref] {
							seen[ref] = true
							c.GoneRefs = append(c.GoneRefs, ref)
						}
					}
				}
			}
			sortRefs(c.GoneRefs)
		}
	}
	sort.SliceStable(r.Grown, func(i, j int) bool {
		di, dj := r.Grown[i].Head-r.Grown[i].Base, r.Grown[j].Head-r.Grown[j].Base
		if di != dj {
			return di > dj
		}
		return r.Grown[i].Metric < r.Grown[j].Metric
	})
	return r
}

// Problems counts findings that make --fail exit 1.
func (r *Report) Problems() int {
	n := len(r.Removed) + len(r.TypeChanges) + len(r.LabelsGone) + len(r.Buckets) + len(r.IDLabels) + len(r.Lost)
	for _, g := range r.Grown {
		if g.OverLimit {
			n++
		}
	}
	return n
}

func sortedNames(s *Scrape) []string {
	names := make([]string, 0, len(s.Families))
	for n := range s.Families {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func setDiff(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func sortLe(les []string) []string {
	sort.SliceStable(les, func(i, j int) bool {
		a, errA := parseValue(les[i])
		b, errB := parseValue(les[j])
		if errA != nil || errB != nil {
			return les[i] < les[j]
		}
		return a < b
	})
	return les
}

// sampleNames lists the names a query could use for a family.
func sampleNames(f *Family) []string {
	names := []string{f.Name}
	for _, suf := range suffixes[f.Type] {
		names = append(names, f.Name+suf)
	}
	if f.Type == "counter" && !strings.HasSuffix(f.Name, "_total") {
		names = append(names, f.Name+"_total")
	}
	return names
}

// driver finds the label whose number of distinct values changed the most.
func driver(b, h *Family) (string, int, int) {
	best, bv, hv, bestDelta := "", 0, 0, 0
	for _, label := range sortedKeys(h.values) {
		d := len(h.values[label]) - len(b.values[label])
		if d < 0 {
			d = -d
		}
		if d > bestDelta {
			best, bv, hv, bestDelta = label, len(b.values[label]), len(h.values[label]), d
		}
	}
	for _, label := range sortedKeys(b.values) {
		if h.values[label] != nil {
			continue
		}
		if d := len(b.values[label]); d > bestDelta {
			best, bv, hv, bestDelta = label, d, 0, d
		}
	}
	return best, bv, hv
}

// goneValues lists label values in base that no series in head has, for
// labels that exist on both sides.
func goneValues(b, h *Family) map[string][]string {
	out := map[string][]string{}
	for _, label := range sortedKeys(b.values) {
		if h.values[label] == nil {
			continue
		}
		if gone := setDiff(b.values[label], h.values[label]); len(gone) > 0 {
			out[label] = gone
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// isInfo reports whether a family describes the build or target, like
// go_info or app_build_info, rather than measuring something.
func isInfo(f *Family) bool {
	return f.Type == "info" || strings.HasSuffix(f.Name, "_info")
}

func hasGoneValues(b, h *Family) bool {
	return goneValues(b, h) != nil
}

// guessRename finds an added family that is probably the removed one under a
// new name: same type and labels, and a name that differs only by a suffix
// or a few characters.
func guessRename(f *Family, added []*Family) string {
	best, bestScore := "", 0
	for _, a := range added {
		if !sameLabelNames(f, a) {
			continue
		}
		score := 0
		switch {
		case a.Name == f.Name+"_total" || f.Name == a.Name+"_total":
			score = 100
		case strings.HasPrefix(a.Name, f.Name) || strings.HasPrefix(f.Name, a.Name) ||
			strings.HasSuffix(a.Name, "_"+f.Name) || strings.HasSuffix(f.Name, "_"+a.Name):
			score = 50
		default:
			d := editDistance(f.Name, a.Name)
			if d <= max(2, len(f.Name)/5) {
				score = 40 - d
			}
		}
		if a.Type == f.Type {
			score += 5
		}
		if score > bestScore || score == bestScore && score > 0 && a.Name < best {
			best, bestScore = a.Name, score
		}
	}
	if bestScore <= 5 {
		return ""
	}
	return best
}

func sameLabelNames(a, b *Family) bool {
	if len(a.values) != len(b.values) {
		return false
	}
	for k := range a.values {
		if b.values[k] == nil {
			return false
		}
	}
	return true
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

var (
	uuidRe   = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	hexRe    = regexp.MustCompile(`(?i)^[0-9a-f]{16,}$`)
	digitsRe = regexp.MustCompile(`^\d{5,}$`)
	ipv4Re   = regexp.MustCompile(`^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}(:\d+)?$`)
	emailRe  = regexp.MustCompile(`^[^@\s/]+@[^@\s/]+\.[a-zA-Z]{2,}$`)
	segIDRe  = regexp.MustCompile(`(?i)^(\d+|[0-9a-f]{16,}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)
)

// LooksLikeID reports whether a label value looks like a user, request or
// object ID, which would give a metric one series per ID.
func LooksLikeID(v string) bool {
	if uuidRe.MatchString(v) || hexRe.MatchString(v) && strings.ContainsAny(v, "0123456789") ||
		digitsRe.MatchString(v) || ipv4Re.MatchString(v) || emailRe.MatchString(v) {
		return true
	}
	if strings.Contains(v, "/") {
		for _, seg := range strings.Split(strings.SplitN(v, "?", 2)[0], "/") {
			if segIDRe.MatchString(seg) {
				return true
			}
		}
	}
	return false
}

// idLabel reports a label whose head values look like IDs when the base had
// no ID-like values for it. baseValues is nil for a label or family that's new.
func idLabel(f *Family, label string, baseValues map[string]bool) *IDLabel {
	for v := range baseValues {
		if LooksLikeID(v) {
			return nil
		}
	}
	var ex []string
	for _, v := range sortedKeys(f.values[label]) {
		if LooksLikeID(v) {
			ex = append(ex, v)
		}
	}
	if len(ex) == 0 {
		return nil
	}
	return &IDLabel{Metric: f.Name, Label: label, Values: len(f.values[label]), Examples: ex[:min(3, len(ex))]}
}

// ---- References ----

// RefIndex holds the identifiers in rules, dashboards and other text files,
// so removed metrics can be traced to what uses them.
type RefIndex struct {
	Files int
	locs  map[string][]Ref
	lines map[Ref]map[string]bool
	text  map[Ref]string
}

var identRe = regexp.MustCompile(`[a-zA-Z_:][a-zA-Z0-9_:]*`)

// skipDirs are never searched for references.
var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, ".hg": true, ".svn": true}

// BuildRefs reads every text file under the given paths.
func BuildRefs(paths []string) (*RefIndex, error) {
	idx := &RefIndex{locs: map[string][]Ref{}, lines: map[Ref]map[string]bool{}, text: map[Ref]string{}}
	for _, root := range paths {
		info, err := os.Stat(root)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			if err := idx.add(root); err != nil {
				return nil, err
			}
			continue
		}
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if p != root && skipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			return idx.add(p)
		})
		if err != nil {
			return nil, err
		}
	}
	return idx, nil
}

func (idx *RefIndex) add(p string) error {
	info, err := os.Stat(p)
	if err != nil {
		return err
	}
	if info.Size() > 20*1024*1024 {
		return nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	if bytes.IndexByte(data[:min(len(data), 8192)], 0) >= 0 {
		return nil // binary
	}
	idx.Files++
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1024*1024), maxLine)
	n := 0
	for sc.Scan() {
		n++
		ref := Ref{File: filepath.ToSlash(p), Line: n}
		ids := identRe.FindAllString(sc.Text(), -1)
		if len(ids) > 0 {
			idx.text[ref] = sc.Text()
		}
		for _, id := range ids {
			if idx.lines[ref] == nil {
				idx.lines[ref] = map[string]bool{}
			}
			if !idx.lines[ref][id] {
				idx.lines[ref][id] = true
				idx.locs[id] = append(idx.locs[id], ref)
			}
		}
	}
	return sc.Err()
}

// find returns the lines that mention any of names, and also label when
// label isn't empty.
func (idx *RefIndex) find(names []string, label string) []Ref {
	if idx == nil {
		return nil
	}
	seen := map[Ref]bool{}
	var out []Ref
	for _, n := range names {
		for _, ref := range idx.locs[n] {
			if seen[ref] || label != "" && !idx.lines[ref][label] {
				continue
			}
			seen[ref] = true
			out = append(out, ref)
		}
	}
	sortRefs(out)
	return out
}

func sortRefs(refs []Ref) {
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].File != refs[j].File {
			return refs[i].File < refs[j].File
		}
		return refs[i].Line < refs[j].Line
	})
}

// findValue returns the lines that mention any of names, the label, and
// the value in quotes, as in {code="500"} or {code=\"500\"} in JSON.
func (idx *RefIndex) findValue(names []string, label, value string) []Ref {
	var out []Ref
	for _, ref := range idx.find(names, label) {
		t := idx.text[ref]
		if strings.Contains(t, `"`+value+`"`) || strings.Contains(t, `\"`+value+`\"`) || strings.Contains(t, "'"+value+"'") {
			out = append(out, ref)
		}
	}
	return out
}

// ---- Output ----

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func formatRefs(refs []Ref) string {
	var parts []string
	for i, r := range refs {
		if i == 5 {
			parts = append(parts, fmt.Sprintf("and %d more", len(refs)-5))
			break
		}
		parts = append(parts, fmt.Sprintf("%s:%d", r.File, r.Line))
	}
	return strings.Join(parts, ", ")
}

func quoteList(vals []string, limit int) string {
	var parts []string
	for i, v := range vals {
		if i == limit {
			parts = append(parts, fmt.Sprintf("and %d more", len(vals)-limit))
			break
		}
		parts = append(parts, strconv.Quote(v))
	}
	return strings.Join(parts, ", ")
}

// WriteText prints the report for people and agents.
func (r *Report) WriteText(w io.Writer) {
	var head []string
	add := func(n int, one, many string) {
		if n > 0 {
			head = append(head, plural(n, one, many))
		}
	}
	over := 0
	for _, g := range r.Grown {
		if g.OverLimit {
			over++
		}
	}
	add(len(r.Removed), "removed metric", "removed metrics")
	add(len(r.TypeChanges), "type change", "type changes")
	add(len(r.LabelsGone), "removed label", "removed labels")
	add(len(r.Buckets), "bucket change", "bucket changes")
	add(len(r.IDLabels), "label with ID-like values", "labels with ID-like values")
	add(over, fmt.Sprintf("metric with over %g%% more series", r.MaxGrowth), fmt.Sprintf("metrics with over %g%% more series", r.MaxGrowth))
	add(len(r.Lost), "metric that lost series", "metrics that lost series")
	add(len(r.Grown)-over, "metric with a few more series", "metrics with a few more series")
	add(len(r.Added), "new metric", "new metrics")
	add(len(r.LabelsAdded), "new label", "new labels")
	for _, warn := range r.Warnings {
		fmt.Fprintf(w, "%s\n\n", wrap("WARNING: "+warn, 90, "  "))
	}
	totals := fmt.Sprintf("Series: %d -> %d. Metrics: %d -> %d.", r.BaseSeries, r.HeadSeries, r.BaseMetrics, r.HeadMetrics)
	if len(head) == 0 {
		fmt.Fprintf(w, "No changes in metric names, types, labels, buckets or series counts (%d metrics, %d series).\n", r.HeadMetrics, r.HeadSeries)
		return
	}
	fmt.Fprintln(w, strings.Join(head, ", "))
	fmt.Fprintln(w, totals)
	section := func(title string) {
		fmt.Fprintf(w, "\n%s\n", title)
	}
	refLine := func(refs []Ref) {
		if len(refs) > 0 {
			fmt.Fprintf(w, "      used in %s\n", formatRefs(refs))
		}
	}
	if len(r.Removed) > 0 {
		section("Removed metrics (queries, alerts and dashboards using them get no data):")
		for _, m := range r.Removed {
			fmt.Fprintf(w, "  %s  %s, %s\n", m.Name, m.Type, plural(m.Series, "series", "series"))
			if m.RenamedTo != "" {
				fmt.Fprintf(w, "      probably renamed to %s\n", m.RenamedTo)
			}
			refLine(m.Refs)
		}
	}
	if len(r.TypeChanges) > 0 {
		section("Type changed (rate() and histogram_quantile() depend on the type):")
		for _, t := range r.TypeChanges {
			fmt.Fprintf(w, "  %s  %s -> %s\n", t.Name, t.Base, t.Head)
			refLine(t.Refs)
		}
	}
	if len(r.LabelsGone) > 0 {
		section("Removed labels (by() and label matchers using them stop matching):")
		for _, l := range r.LabelsGone {
			fmt.Fprintf(w, "  %s  label %s, which had %s\n", l.Metric, l.Label, plural(l.Values, "value", "values"))
			refLine(l.Refs)
		}
	}
	if len(r.Buckets) > 0 {
		section("Histogram buckets changed (quantiles across the change and le= matchers are affected):")
		for _, b := range r.Buckets {
			var parts []string
			if len(b.Removed) > 0 {
				parts = append(parts, "removed le "+strings.Join(b.Removed, ", "))
			}
			if len(b.Added) > 0 {
				parts = append(parts, "added le "+strings.Join(b.Added, ", "))
			}
			fmt.Fprintf(w, "  %s  %s\n", b.Metric, strings.Join(parts, "; "))
			refLine(b.Refs)
		}
	}
	if len(r.IDLabels) > 0 {
		section("Label values that look like IDs (one series per ID, which grows without limit):")
		for _, l := range r.IDLabels {
			fmt.Fprintf(w, "  %s  label %s: %s (%s)\n", l.Metric, l.Label, quoteList(l.Examples, 3), plural(l.Values, "value", "values"))
		}
	}
	if over > 0 {
		section(fmt.Sprintf("Over %g%% more series:", r.MaxGrowth))
		for _, g := range r.Grown {
			if g.OverLimit {
				writeGrowth(w, g)
			}
		}
	}
	if len(r.Lost) > 0 {
		section("Lost series (alerts and dashboards that select them get no data):")
		for _, c := range r.Lost {
			writeGrowth(w, c)
		}
	}
	if len(r.Grown) > over {
		section("A few more series:")
		for _, g := range r.Grown {
			if !g.OverLimit {
				writeGrowth(w, g)
			}
		}
	}
	if len(r.Added) > 0 {
		section("New metrics:")
		for _, a := range r.Added {
			fmt.Fprintf(w, "  %s  %s, %s\n", a.Name, a.Type, plural(a.Series, "series", "series"))
		}
	}
	if len(r.LabelsAdded) > 0 {
		section("New labels on existing metrics:")
		for _, l := range r.LabelsAdded {
			fmt.Fprintf(w, "  %s  label %s, %s\n", l.Metric, l.Label, plural(l.Values, "value", "values"))
		}
	}
	if r.RefsSearched > 0 {
		fmt.Fprintf(w, "\nSearched %s for references.\n", plural(r.RefsSearched, "file", "files"))
	}
}

// wrap breaks text into lines of at most width characters where it can,
// indenting every line after the first.
func wrap(text string, width int, indent string) string {
	var b strings.Builder
	n := 0
	for i, word := range strings.Fields(text) {
		if i > 0 && n+1+len(word) > width {
			b.WriteString("\n" + indent)
			n = len(indent)
		} else if i > 0 {
			b.WriteByte(' ')
			n++
		}
		b.WriteString(word)
		n += len(word)
	}
	return b.String()
}

func writeGrowth(w io.Writer, g SeriesChange) {
	fmt.Fprintf(w, "  %s  %d -> %d series", g.Metric, g.Base, g.Head)
	if g.Driver != "" {
		fmt.Fprintf(w, ", label %s %d -> %d values", g.Driver, g.BaseValues, g.HeadValues)
	}
	fmt.Fprintln(w)
	for _, label := range sortedKeys(g.Gone) {
		fmt.Fprintf(w, "      %s no longer has %s\n", label, quoteList(g.Gone[label], 5))
	}
	if len(g.GoneRefs) > 0 {
		fmt.Fprintf(w, "      used in %s\n", formatRefs(g.GoneRefs))
	}
}

// ---- Command line ----

const usage = `seriesdiff compares two Prometheus /metrics scrapes, such as one from the
base branch and one from a pull request, run with the same workload.

Usage:
  seriesdiff compare [--refs PATH]... [--max-growth N] [--fail] [--json] [--trust-ends] BASE HEAD
  seriesdiff version

BASE and HEAD are files saved from a /metrics endpoint in the Prometheus
text format or OpenMetrics. Use - to read one of them from standard input.

Flags:
  --refs PATH         a file or directory of alert rules, recording rules or
                      dashboards to search for metrics that changed (repeatable)
  --max-growth N      percent growth in one metric's series that counts as a
                      problem (default 50)
  --fail              exit 1 when there are removed metrics, type changes,
                      removed labels, bucket changes, ID-like label values,
                      lost series, or growth over --max-growth
  --json              print the full result as JSON
  --trust-ends        don't warn and exit 2 when one file stops where the
                      other goes on, which is how a cut-off file looks

Exit codes: 0 compared, 1 problems found with --fail, 2 bad usage, an
input that isn't a complete scrape, or one that may be cut off (the report
is still printed, after a warning).
`

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, usage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	switch args[0] {
	case "version", "--version":
		fmt.Fprintln(stdout, "seriesdiff", version)
		return 0
	case "compare":
	default:
		fmt.Fprintf(stderr, "seriesdiff: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
	fl := flag.NewFlagSet("compare", flag.ContinueOnError)
	fl.SetOutput(stderr)
	var refs multiFlag
	fl.Var(&refs, "refs", "")
	maxGrowth := fl.Float64("max-growth", 50, "")
	fail := fl.Bool("fail", false, "")
	asJSON := fl.Bool("json", false, "")
	trustEnds := fl.Bool("trust-ends", false, "")
	fl.Usage = func() { fmt.Fprint(stderr, usage) }
	if err := fl.Parse(reorder(args[1:])); err != nil {
		return 2
	}
	if fl.NArg() != 2 {
		fmt.Fprintf(stderr, "seriesdiff: compare needs two files, BASE and HEAD\n\n%s", usage)
		return 2
	}
	if *maxGrowth < 0 || math.IsNaN(*maxGrowth) || math.IsInf(*maxGrowth, 0) {
		fmt.Fprintln(stderr, "seriesdiff: --max-growth must be zero or more")
		return 2
	}
	if fl.Arg(0) == "-" && fl.Arg(1) == "-" {
		fmt.Fprintln(stderr, "seriesdiff: only one of BASE and HEAD can be -")
		return 2
	}
	var scrapes [2]*Scrape
	failed := false
	for i, p := range fl.Args() {
		side := [2]string{"base", "head"}[i]
		var data []byte
		var err error
		if p == "-" {
			data, err = io.ReadAll(stdin)
		} else {
			data, err = os.ReadFile(p)
		}
		if err != nil {
			fmt.Fprintf(stderr, "seriesdiff: can't read %s: %v\n", side, err)
			failed = true
			continue
		}
		name := p
		if p == "-" {
			name = "standard input"
		}
		s, err := Parse(name, data)
		if err != nil {
			fmt.Fprintf(stderr, "seriesdiff: %s is not a usable scrape, so nothing was compared.\n  %v\n", side, err)
			failed = true
			continue
		}
		scrapes[i] = s
	}
	if failed {
		return 2
	}
	var warnings []string
	if !*trustEnds {
		for i, side := range [2]string{"base", "head"} {
			s, other := scrapes[i], scrapes[1-i]
			if last, more, early := s.EndsEarly(other); early {
				warnings = append(warnings, fmt.Sprintf("%s may be cut off, so this comparison can't be trusted. %s ends with %s. "+
					"%s has that series too, and then %d more that %s doesn't have at all, which is what a scrape cut off at a line break looks like. "+
					"If the change really %s only series that sort after it, check that %s is complete and run again with --trust-ends.",
					side, s.Path, short(last), other.Path, more, side, [2]string{"adds", "removes"}[i], side))
			}
		}
	}
	opt := Options{MaxGrowth: *maxGrowth}
	if len(refs) > 0 {
		idx, err := BuildRefs(refs)
		if err != nil {
			fmt.Fprintf(stderr, "seriesdiff: can't read --refs: %v\n", err)
			return 2
		}
		if idx.Files == 0 {
			fmt.Fprintf(stderr, "seriesdiff: --refs %s has no text files to search\n", strings.Join(refs, ", "))
			return 2
		}
		opt.Refs = idx
	}
	r := Compare(scrapes[0], scrapes[1], opt)
	r.Warnings = warnings
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(r); err != nil {
			fmt.Fprintf(stderr, "seriesdiff: %v\n", err)
			return 2
		}
	} else {
		r.WriteText(stdout)
	}
	if len(warnings) > 0 {
		return 2
	}
	if *fail && r.Problems() > 0 {
		return 1
	}
	return 0
}

// reorder moves flags before positional arguments, so flags can go anywhere.
func reorder(args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if a == "-" || !strings.HasPrefix(a, "-") {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if (name == "refs" || name == "max-growth") && i+1 < len(args) {
			flags = append(flags, args[i+1])
			i++
		}
	}
	return append(flags, pos...)
}
