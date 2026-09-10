package main

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
)

var version = "dev"
var commit = "unknown"
var buildDate = "unknown"

// Report discovery is portable; local preferences never enter the repository.
func defaultReportDir() string {
	if dir := os.Getenv("SPXQ_REPORT_DIR"); dir != "" {
		return dir
	}
	if config, err := os.UserConfigDir(); err == nil {
		if data, err := os.ReadFile(filepath.Join(config, "spxq", "report-dir")); err == nil {
			if dir := strings.TrimSpace(string(data)); dir != "" {
				return dir
			}
		}
	}
	return "."
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "spxq:", err)
		os.Exit(1)
	}
}
func usage(w io.Writer) {
	fmt.Fprintln(w, `spxq — streaming SPX call-tree explorer

  spxq reports [--dir PATH] [--limit 30]
  spxq import REPORT.json [REPORT.txt.gz|.txt.zst|.txt] -o REPORT.db
  spxq view [REPORT.db|REPORT.json|REPORT_KEY] [--dir PATH] [--theme dark|light]
  spxq tree REPORT.db [--root 1 --depth 3 --limit 20 --metric wt]
  spxq flat REPORT.db [--metric wt --sort exc --limit 50]
  spxq search REPORT.db 'Doctrine*' [--limit 50]
  spxq callers REPORT.db '*PDOStatement::execute*'

view without an argument opens a report picker. JSON/key input is cached locally.
View keys: arrows expand/collapse/select · Enter focus · Backspace back · Home report
          PgDn more · / search · m metric · i inclusive/exclusive · s sort · q quit
All processing stays local; source reports are read-only. No login or telemetry.`)
}

// Accept flags before or after positional arguments, as shown in the shared design.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-h" || a == "--help" {
			usage(fs.Output())
			return nil, flag.ErrHelp
		}
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			key := strings.TrimLeft(strings.SplitN(a, "=", 2)[0], "-")
			f := fs.Lookup(key)
			if f == nil {
				return nil, fmt.Errorf("unknown flag %s", a)
			}
			b, ok := f.Value.(interface{ IsBoolFlag() bool })
			isBool := ok && b.IsBoolFlag()
			if !strings.Contains(a, "=") && !isBool {
				if i+1 >= len(args) {
					return nil, fmt.Errorf("missing value for %s", a)
				}
				i++
				flags = append(flags, args[i])
			}
		} else {
			pos = append(pos, a)
		}
	}
	return pos, fs.Parse(flags)
}

type reportEntry struct {
	path string
	m    metadata
	size int64
}

func (m metadata) target() string {
	if m.CLI != 0 {
		return m.Command
	}
	target := m.URI
	if !strings.Contains(target, "://") && m.HTTPHost != "" {
		target = m.HTTPHost + "/" + strings.TrimPrefix(target, "/")
	}
	if m.Method != "" {
		target = m.Method + " " + target
	}
	return target
}

func listReports(dir string) ([]reportEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var rs []reportEntry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		m, e2 := readMetadata(p)
		if e2 != nil {
			continue
		}
		body, e2 := reportBody(p)
		if e2 != nil {
			continue
		}
		st, e2 := os.Stat(body)
		if e2 != nil {
			continue
		}
		rs = append(rs, reportEntry{p, m, st.Size()})
	}
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].m.Timestamp == rs[j].m.Timestamp {
			return rs[i].path > rs[j].path
		}
		return rs[i].m.Timestamp > rs[j].m.Timestamp
	})
	return rs, nil
}
func run(ctx context.Context, args []string, out, errout io.Writer) error {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "version") {
		fmt.Fprintf(out, "spxq %s (%s, %s)\n", version, commit, buildDate)
		return nil
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		usage(out)
		return nil
	}
	cmd := args[0]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(errout)
	dir := fs.String("dir", defaultReportDir(), "report directory")
	limit := fs.Int("limit", 30, "maximum siblings/results")
	depth := fs.Int("depth", 3, "tree depth")
	root := fs.Int64("root", 1, "call-tree node ID")
	metric := fs.String("metric", "", "recorded metric")
	sortBy := fs.String("sort", "inc", "inc, exc or calls")
	output := fs.String("o", "", "output database")
	allow := fs.Bool("allow-incomplete", false, "close unclosed frames at last event")
	themeName := fs.String("theme", "dark", "dark or light terminal theme")
	cache := fs.String("cache", "", "cache directory (default: user cache/spxq)")
	pos, err := parseArgs(fs, args[1:])
	if err == flag.ErrHelp {
		return nil
	}
	if err != nil {
		return err
	}
	theme, err := newTheme(*themeName)
	if err != nil {
		return err
	}
	if *limit < 1 || *limit > 10000 || *depth < 0 || *depth > 100 {
		return fmt.Errorf("limit must be 1..10000 and depth 0..100")
	}
	if *sortBy != "inc" && *sortBy != "exc" && *sortBy != "calls" {
		return fmt.Errorf("sort must be inc, exc or calls")
	}
	switch cmd {
	case "reports":
		if len(pos) > 0 {
			return fmt.Errorf("reports takes no positional arguments")
		}
		rs, e := listReports(*dir)
		if e != nil {
			return e
		}
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "REPORT KEY\tDATE\tHOST\tCOMMAND / URL\tCALLS\tWALL\tCOMPRESSED\tCUSTOM METADATA")
		for i, r := range rs {
			if i >= *limit {
				break
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%.3fs\t%.1fMiB\t%s\n", strings.TrimSuffix(filepath.Base(r.path), ".json"), time.Unix(r.m.Timestamp, 0).Format("2006-01-02 15:04"), clean(r.m.Host), clean(r.m.target()), count(r.m.Calls), r.m.Wall/1000, float64(r.size)/1048576, clean(r.m.Custom))
		}
		return w.Flush()
	case "import":
		if len(pos) < 1 || len(pos) > 2 {
			return fmt.Errorf("import requires JSON and optional body")
		}
		body := ""
		if len(pos) == 2 {
			body = pos[1]
		} else {
			body, err = reportBody(pos[0])
			if err != nil {
				return err
			}
		}
		if *output == "" {
			*output = filepath.Base(strings.TrimSuffix(pos[0], ".json")) + ".db"
		}
		return importReport(ctx, pos[0], body, *output, *allow, errout)
	case "view", "tree", "flat", "search", "callers":
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
	if cmd == "view" && len(pos) == 0 {
		rs, e := listReports(*dir)
		if e != nil {
			return e
		}
		p, e := pickReport(rs, theme)
		if e != nil {
			return e
		}
		if p == "" {
			return nil
		}
		pos = []string{p}
	}
	expected := 1
	if cmd == "search" || cmd == "callers" {
		expected = 2
	}
	if len(pos) != expected {
		return fmt.Errorf("%s requires %d argument(s)", cmd, expected)
	}
	path := pos[0]
	if cmd == "view" && !strings.HasSuffix(path, ".db") {
		if !strings.HasSuffix(path, ".json") {
			path = filepath.Join(*dir, path+".json")
		}
		body, e := reportBody(path)
		if e != nil {
			return e
		}
		if *cache == "" {
			c, e := os.UserCacheDir()
			if e != nil {
				return e
			}
			*cache = filepath.Join(c, "spxq")
		}
		abs, e := filepath.Abs(path)
		if e != nil {
			return e
		}
		st, e := os.Stat(body)
		if e != nil {
			return e
		}
		mt, e := os.Stat(path)
		if e != nil {
			return e
		}
		hash := sha256.Sum256([]byte(fmt.Sprintf("v2:%s:%d:%d:%d:%d:%t", abs, st.Size(), st.ModTime().UnixNano(), mt.Size(), mt.ModTime().UnixNano(), *allow)))
		dest := filepath.Join(*cache, fmt.Sprintf("%x.db", hash[:16]))
		if _, e = os.Stat(dest); os.IsNotExist(e) {
			fmt.Fprintln(errout, "Indexing", filepath.Base(path))
			if e = importReport(ctx, path, body, dest, *allow, errout); e != nil {
				return e
			}
		}
		path = dest
	}
	r, err := openReport(path)
	if err != nil {
		return err
	}
	defer r.db.Close()
	r.sort = *sortBy
	if err = r.setMetric(*metric); err != nil {
		return err
	}
	if cmd == "view" {
		return view(r, *root, *limit, theme)
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	defer w.Flush()
	fmt.Fprintf(w, "ID\tCALLS\tINCLUSIVE (%s)\tEXCLUSIVE\tFUNCTION\n", r.metrics[r.metric])
	printNode := func(n node, prefix string) {
		fmt.Fprintf(w, "%d\t%d\t%s\t%s\t%s%s\n", n.id, n.calls, formatValue(n.inc, r.metrics[r.metric]), formatValue(n.exc, r.metrics[r.metric]), prefix, clean(n.name))
	}
	switch cmd {
	case "tree":
		n, e := r.get(*root)
		if e != nil {
			return e
		}
		printNode(n, "")
		var walk func(int64, int) error
		walk = func(id int64, d int) error {
			if d > *depth {
				return nil
			}
			ns, hidden, sum, e := r.children(id, 0, *limit)
			if e != nil {
				return e
			}
			for _, n := range ns {
				printNode(n, strings.Repeat("  ", d))
				if e = walk(n.id, d+1); e != nil {
					return e
				}
			}
			if hidden > 0 {
				fmt.Fprintf(w, "\t\t%s\t\t%s… %d more\n", formatValue(sum, r.metrics[r.metric]), strings.Repeat("  ", d), hidden)
			}
			return nil
		}
		return walk(n.id, 1)
	case "search":
		ns, e := r.search(pos[1], *limit)
		if e != nil {
			return e
		}
		for _, n := range ns {
			printNode(n, "")
		}
	default:
		pattern := ""
		if cmd == "callers" {
			pattern = pos[1]
		}
		ns, e := r.flat(pattern, cmd == "callers", *limit)
		if e != nil {
			return e
		}
		for _, n := range ns {
			printNode(n, "")
		}
	}
	return nil
}
