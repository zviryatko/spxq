package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func fixture(t *testing.T, body, ext string) (string, string, string) {
	t.Helper()
	d := t.TempDir()
	meta := filepath.Join(d, "sample.json")
	p := filepath.Join(d, "sample"+ext)
	if e := os.WriteFile(meta, []byte(`{"enabled_metrics":["wt","zm"],"recorded_call_count":4}`), 0600); e != nil {
		t.Fatal(e)
	}
	var b bytes.Buffer
	switch ext {
	case ".txt.gz":
		w := gzip.NewWriter(&b)
		w.Write([]byte(body))
		w.Close()
	case ".txt.zst":
		w, e := zstd.NewWriter(&b)
		if e != nil {
			t.Fatal(e)
		}
		w.Write([]byte(body))
		w.Close()
	default:
		b.WriteString(body)
	}
	if e := os.WriteFile(p, b.Bytes(), 0600); e != nil {
		t.Fatal(e)
	}
	return meta, p, filepath.Join(d, "report.db")
}

const recursiveTrace = `[events]
0 1 0 0
1 1 10 10
1 1 20 20
1 0 30 15
1 0 50 5
1 1 60 5
1 0 80 0
0 0 100 0
[functions]
main
A
`

func TestAggregation(t *testing.T) {
	for _, ext := range []string{".txt", ".txt.gz", ".txt.zst"} {
		t.Run(ext, func(t *testing.T) {
			m, b, out := fixture(t, recursiveTrace, ext)
			if e := importReport(context.Background(), m, b, out, false, nil); e != nil {
				t.Fatal(e)
			}
			r, e := openReport(out)
			if e != nil {
				t.Fatal(e)
			}
			defer r.db.Close()
			ns, hidden, sum, e := r.children(2, 0, 1)
			if e != nil || hidden != 0 || sum != 0 || len(ns) != 1 {
				t.Fatalf("children: %+v %d %g %v", ns, hidden, sum, e)
			}
			a := ns[0]
			if a.calls != 2 || a.inc != 60 || a.exc != 50 {
				t.Fatalf("repeated path: %+v", a)
			}
			flat, e := r.flat("", false, 10)
			if e != nil {
				t.Fatal(e)
			}
			for _, n := range flat {
				if n.name == "A" && (n.inc != 60 || n.exc != 60 || n.calls != 3) {
					t.Fatalf("recursive flat: %+v", n)
				}
			}
			r.setMetric("zm")
			n, e := r.get(a.id)
			if e != nil || n.inc != -10 || n.exc != -5 {
				t.Fatalf("signed memory: %+v %v", n, e)
			}
			if e = importReport(context.Background(), m, b, out, false, nil); e == nil {
				t.Fatal("overwrote output")
			}
		})
	}
}
func TestCompressedEncoding(t *testing.T) {
	p := eventParser{metrics: 2}
	lines := []string{"0|0|a0|a0", "a|1|10|10", "b|r1|10|10", "-r1|10|-5", "-r1|20|-10", "a|1|10|", "-r1|20|-5", "-r7|20|"}
	legacy := eventParser{metrics: 2}
	expected := strings.Split(strings.TrimSpace(strings.Split(recursiveTrace, "[functions]")[0]), "\n")[1:]
	for i, line := range lines {
		got, e := p.parse(line)
		if e != nil {
			t.Fatal(e)
		}
		want, e := legacy.parse(expected[i])
		if e != nil {
			t.Fatal(e)
		}
		if got.fid != want.fid || got.start != want.start || got.values[0] != want.values[0] || got.values[1] != want.values[1] {
			t.Fatalf("line %d: %+v != %+v", i, got, want)
		}
	}
	body := "[events]\n" + strings.Join(lines, "\n") + "\n[functions]\nmain\nA\n"
	m, b, out := fixture(t, body, ".txt.zst")
	if e := importReport(context.Background(), m, b, out, false, nil); e != nil {
		t.Fatal(e)
	}
}
func TestInvalidTraceAtomic(t *testing.T) {
	for name, body := range map[string]string{"mismatch": "[events]\n0 1 0 0\n1 0 1 0\n[functions]\nmain\n", "incomplete": "[events]\n0 1 0 0\n[functions]\nmain\n", "missing function": "[events]\n2 1 0 0\n2 0 1 0\n[functions]\nmain\n", "truncated": "[events]\n0 1 0 0\n", "bad number": "[events]\n0 1 invalid 0\n[functions]\nmain\n"} {
		t.Run(name, func(t *testing.T) {
			m, b, out := fixture(t, body, ".txt.gz")
			if e := importReport(context.Background(), m, b, out, false, nil); e == nil {
				t.Fatal("accepted malformed trace")
			}
			if _, e := os.Stat(out); !os.IsNotExist(e) {
				t.Fatal("partial output published")
			}
		})
	}
}
func TestIncompleteOptIn(t *testing.T) {
	m, b, out := fixture(t, "[events]\n0 1 0 0\n1 1 10 5\n1 0 20 3\n[functions]\nmain\nchild\n", ".txt")
	if e := importReport(context.Background(), m, b, out, true, nil); e != nil {
		t.Fatal(e)
	}
	r, e := openReport(out)
	if e != nil {
		t.Fatal(e)
	}
	defer r.db.Close()
	n, e := r.get(2)
	if e != nil || n.inc != 20 || n.exc != 10 {
		t.Fatalf("%+v %v", n, e)
	}
}
func TestPagination(t *testing.T) {
	m, b, out := fixture(t, "[events]\n0 1 0 0\n1 1 0 0\n1 0 20 0\n2 1 20 0\n2 0 30 0\n0 0 40 0\n[functions]\nmain\na\nb\n", ".txt")
	if e := importReport(context.Background(), m, b, out, false, nil); e != nil {
		t.Fatal(e)
	}
	r, e := openReport(out)
	if e != nil {
		t.Fatal(e)
	}
	defer r.db.Close()
	ns, hidden, sum, e := r.children(2, 0, 1)
	if e != nil || len(ns) != 1 || ns[0].name != "a" || hidden != 1 || sum != 10 {
		t.Fatalf("%+v %d %g %v", ns, hidden, sum, e)
	}
	ns, hidden, sum, e = r.children(2, 1, 1)
	if e != nil || len(ns) != 1 || ns[0].name != "b" || hidden != 0 || sum != 0 {
		t.Fatalf("%+v %d %g %v", ns, hidden, sum, e)
	}
}
func TestParserRejectsBadRefs(t *testing.T) {
	for _, line := range []string{"-r1|0|0", "x|0|0|0", "0|0|x|0", "0 2 0 0"} {
		p := eventParser{metrics: 2}
		if _, e := p.parse(line); e == nil {
			t.Fatalf("accepted %q", line)
		}
	}
}
func TestFunctionLocation(t *testing.T) {
	name, file, line := parseFunction("phar:///app.phar/x.php:42:Foo::bar")
	if name != "Foo::bar" || file != "phar:///app.phar/x.php" || line != 42 {
		t.Fatal(name, file, line)
	}
}
func TestCanceledImport(t *testing.T) {
	m, b, out := fixture(t, recursiveTrace, ".txt")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := importReport(ctx, m, b, out, false, nil); e == nil {
		t.Fatal("ignored cancellation")
	}
	if _, e := os.Stat(out); !os.IsNotExist(e) {
		t.Fatal("published canceled import")
	}
}

func TestUnits(t *testing.T) {
	for _, c := range []struct {
		v       float64
		m, want string
	}{{181167027, "wt", "181.167s"}, {1000, "ct", "1.00ms"}, {-5, "it", "-5µs"}, {1024, "zmab", "1.00KiB"}, {1024, "zmac", "1024"}} {
		if got := formatValue(c.v, c.m); got != c.want {
			t.Fatalf("%s: %s != %s", c.m, got, c.want)
		}
	}
}

func TestFractionalMetrics(t *testing.T) {
	bodies := map[string]string{
		"legacy":     "[events]\n0 1 0 -1631.9999\n1 1 0.25 -1632.4999\n1 0 1.5 -1631.7499\n2 1 1.75 -1631.7499\n2 0 2 -1631.6249\n0 0 2.75 -1631.1249\n[functions]\nmain\nchild\nsibling\n",
		"compressed": "[events]\n0|0|a0|a-1631.9999\n0|1|0.25|-0.5\n-r1|1.25|0.75\n0|2|0.25|\n-r1|0.25|0.125\n-r5|0.75|0.5\n[functions]\nmain\nchild\nsibling\n",
	}
	near := func(got, want float64) {
		t.Helper()
		if math.Abs(got-want) > 1e-9 {
			t.Fatalf("got %.12f, want %.12f", got, want)
		}
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			m, b, out := fixture(t, body, ".txt.gz")
			if err := importReport(context.Background(), m, b, out, false, nil); err != nil {
				t.Fatal(err)
			}
			r, err := openReport(out)
			if err != nil {
				t.Fatal(err)
			}
			defer r.db.Close()
			n, err := r.get(2)
			if err != nil {
				t.Fatal(err)
			}
			near(n.inc, 2.75)
			near(n.exc, 1.25)
			ns, hidden, sum, err := r.children(2, 0, 1)
			if err != nil || hidden != 1 || len(ns) != 1 {
				t.Fatalf("pagination: %+v %d %v", ns, hidden, err)
			}
			near(ns[0].inc, 1.25)
			near(sum, .25)
			if err = r.setMetric("zm"); err != nil {
				t.Fatal(err)
			}
			n, err = r.get(2)
			if err != nil {
				t.Fatal(err)
			}
			near(n.inc, .875)
			near(n.exc, 0)
			ns, err = r.flat("", false, 10)
			if err != nil {
				t.Fatal(err)
			}
			for _, n := range ns {
				if n.name == "child" {
					near(n.inc, .75)
					near(n.exc, .75)
				}
			}
			ns, err = r.flat("child", true, 10)
			if err != nil || len(ns) != 1 {
				t.Fatalf("callers: %+v %v", ns, err)
			}
			near(ns[0].inc, .75)
			ns, err = r.search("child", 10)
			if err != nil || len(ns) != 1 {
				t.Fatalf("search: %+v %v", ns, err)
			}
			near(ns[0].inc, .75)
		})
	}
	p := eventParser{metrics: 2}
	e, err := p.parse("70 0 227172 -1631.9999")
	if err != nil {
		t.Fatal(err)
	}
	near(e.values[1], -1631.9999)
	if got := formatValue(.125, "wt"); got != "0.125µs" {
		t.Fatal(got)
	}
	if got := formatValue(-.125, "zm"); got != "-0.125B" {
		t.Fatal(got)
	}
}

func TestNonFiniteMetricsAndFractionalIDs(t *testing.T) {
	for _, line := range []string{"0 1 NaN 0", "0 1 +Inf 0", "0 1 -Inf 0", "0 1 1e999 0", "1.5 1 0 0", "0|0|aNaN|0", "0|0|aInf|0", "0|1.5|0|0"} {
		p := eventParser{metrics: 2}
		if _, err := p.parse(line); err == nil {
			t.Fatalf("accepted %q", line)
		}
	}
}
