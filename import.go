package main

import (
	"bufio"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	_ "github.com/mattn/go-sqlite3"
)

type metadata struct {
	Key       string   `json:"key"`
	Metrics   []string `json:"enabled_metrics"`
	Calls     int64    `json:"recorded_call_count"`
	Wall      float64  `json:"wall_time_ms"` // SPX stores microseconds despite the field name.
	Timestamp int64    `json:"exec_ts"`
	Custom    string   `json:"custom_metadata_str"`
	Host      string   `json:"host_name"`
	CLI       int      `json:"cli"`
	Command   string   `json:"cli_command_line"`
	URI       string   `json:"http_request_uri"`
	HTTPHost  string   `json:"http_host"`
	Method    string   `json:"http_method"`
}
type pathKey struct{ parent, fid int64 }
type frame struct {
	id, fid         int64
	start, children []float64
	outer           bool
}
type total struct {
	calls          int64
	inc, exc, flat []float64
}

func newTotal(n int) *total {
	return &total{inc: make([]float64, n), exc: make([]float64, n), flat: make([]float64, n)}
}
func readMetadata(path string) (metadata, error) {
	var m metadata
	b, e := os.ReadFile(path)
	if e != nil {
		return m, e
	}
	e = json.Unmarshal(b, &m)
	if e == nil && len(m.Metrics) == 0 {
		e = fmt.Errorf("no enabled_metrics")
	}
	seen := map[string]bool{}
	for _, v := range m.Metrics {
		if v == "" || seen[v] {
			return m, fmt.Errorf("empty or duplicate metric")
		}
		seen[v] = true
	}
	return m, e
}
func reportBody(meta string) (string, error) {
	base := strings.TrimSuffix(meta, ".json")
	for _, ext := range []string{".txt.gz", ".txt.zst", ".txt"} {
		if _, err := os.Stat(base + ext); err == nil {
			return base + ext, nil
		}
	}
	return "", fmt.Errorf("no body for %s", meta)
}
func openBody(path string) (io.Reader, func(), error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, nil, e
	}
	switch {
	case strings.HasSuffix(path, ".gz"):
		r, e := gzip.NewReader(f)
		if e != nil {
			f.Close()
			return nil, nil, e
		}
		return r, func() { r.Close(); f.Close() }, nil
	case strings.HasSuffix(path, ".zst"):
		r, e := zstd.NewReader(f, zstd.WithDecoderConcurrency(1))
		if e != nil {
			f.Close()
			return nil, nil, e
		}
		return r, func() { r.Close(); f.Close() }, nil
	default:
		return f, func() { f.Close() }, nil
	}
}
func importReport(ctx context.Context, meta, body, out string, allowIncomplete bool, progress io.Writer) (err error) {
	m, err := readMetadata(meta)
	if err != nil {
		return err
	}
	r, closeBody, err := openBody(body)
	if err != nil {
		return err
	}
	defer closeBody()
	if _, e := os.Stat(out); e == nil {
		return fmt.Errorf("output exists: %s", out)
	} else if !os.IsNotExist(e) {
		return e
	}
	if err = os.MkdirAll(filepath.Dir(out), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(out), ".spxq-import-*.db")
	if err != nil {
		return err
	}
	tmp := f.Name()
	f.Close()
	defer os.Remove(tmp)
	db, err := sql.Open("sqlite3", tmp)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	cols := ""
	for i := range m.Metrics {
		cols += fmt.Sprintf(",inc_%d REAL NOT NULL DEFAULT 0,exc_%d REAL NOT NULL DEFAULT 0,flat_%d REAL NOT NULL DEFAULT 0", i, i, i)
	}
	_, err = db.Exec(`PRAGMA journal_mode=DELETE; PRAGMA synchronous=NORMAL; PRAGMA cache_size=-32768;
 CREATE TABLE info(key TEXT PRIMARY KEY,value TEXT NOT NULL);
 CREATE TABLE functions(id INTEGER PRIMARY KEY,name TEXT NOT NULL,file TEXT NOT NULL,line INTEGER NOT NULL);
 CREATE TABLE nodes(id INTEGER PRIMARY KEY,parent_id INTEGER NOT NULL,function_id INTEGER NOT NULL,calls INTEGER NOT NULL DEFAULT 0` + cols + `,UNIQUE(parent_id,function_id));
 INSERT INTO functions VALUES(-1,'[report]','',0); INSERT INTO nodes(id,parent_id,function_id) VALUES(1,0,-1);`)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { tx.Rollback() }()
	metricsJSON, _ := json.Marshal(m.Metrics)
	for k, v := range map[string]string{"schema": "2", "metrics": string(metricsJSON), "source": meta} {
		if _, err = tx.Exec("INSERT INTO info VALUES(?,?)", k, v); err != nil {
			return err
		}
	}
	paths := map[pathKey]int64{}
	pending := map[int64]*total{}
	stack := []frame{}
	active := map[int64]int{}
	parser := eventParser{metrics: len(m.Metrics)}
	update := "UPDATE nodes SET calls=calls+?"
	for i := range m.Metrics {
		update += fmt.Sprintf(",inc_%d=inc_%d+?,exc_%d=exc_%d+?,flat_%d=flat_%d+?", i, i, i, i, i, i)
	}
	update += " WHERE id=?"
	flush := func() error {
		stmt, e := tx.Prepare(update)
		if e != nil {
			return e
		}
		defer stmt.Close()
		for id, t := range pending {
			args := []any{t.calls}
			for i := range m.Metrics {
				args = append(args, t.inc[i], t.exc[i], t.flat[i])
			}
			args = append(args, id)
			if _, e = stmt.Exec(args...); e != nil {
				return e
			}
		}
		clear(pending)
		return nil
	}
	finish := func(values []float64) {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		active[f.fid]--
		t := pending[f.id]
		if t == nil {
			t = newTotal(len(m.Metrics))
			pending[f.id] = t
		}
		t.calls++
		for i, v := range values {
			delta := v - f.start[i]
			t.inc[i] += delta
			t.exc[i] += delta - f.children[i]
			if f.outer {
				t.flat[i] += delta
			}
			if len(stack) > 0 {
				stack[len(stack)-1].children[i] += delta
			}
		}
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	section := ""
	lineNo := 0
	functions := int64(0)
	events := int64(0)
	calls := int64(0)
	incomplete := 0
	var last []float64
	hadEvents, hadFunctions := false, false
	started := time.Now()
	lastProgress := started
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			continue
		}
		if line == "[events]" {
			if hadEvents || hadFunctions {
				return fmt.Errorf("line %d: duplicate/out-of-order events", lineNo)
			}
			hadEvents = true
			section = "events"
			continue
		}
		if line == "[functions]" {
			if !hadEvents || hadFunctions {
				return fmt.Errorf("line %d: invalid functions section", lineNo)
			}
			hadFunctions = true
			section = "functions"
			if len(stack) > 0 {
				if !allowIncomplete {
					return fmt.Errorf("%d unclosed frames (use --allow-incomplete to close at last event)", len(stack))
				}
				incomplete = len(stack)
				for len(stack) > 0 {
					finish(last)
				}
			}
			continue
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		switch section {
		case "events":
			e, eerr := parser.parse(line)
			if eerr != nil {
				return fmt.Errorf("line %d: %w", lineNo, eerr)
			}
			last = e.values
			events++
			if e.start {
				parent := int64(1)
				if len(stack) > 0 {
					parent = stack[len(stack)-1].id
				}
				key := pathKey{parent, e.fid}
				id, ok := paths[key]
				if !ok {
					err = tx.QueryRow("SELECT id FROM nodes WHERE parent_id=? AND function_id=?", parent, e.fid).Scan(&id)
					if err == sql.ErrNoRows {
						res, e2 := tx.Exec("INSERT INTO nodes(parent_id,function_id) VALUES(?,?)", parent, e.fid)
						if e2 != nil {
							return e2
						}
						id, err = res.LastInsertId()
					}
					if err != nil {
						return err
					}
					if len(paths) >= 100000 {
						clear(paths)
					}
					paths[key] = id
				}
				stack = append(stack, frame{id, e.fid, e.values, make([]float64, len(m.Metrics)), active[e.fid] == 0})
				active[e.fid]++
				calls++
			} else {
				if len(stack) == 0 || stack[len(stack)-1].fid != e.fid {
					return fmt.Errorf("line %d: mismatched exit for function %d", lineNo, e.fid)
				}
				finish(e.values)
			}
			if events%200000 == 0 {
				if err = flush(); err != nil {
					return err
				}
				if err = tx.Commit(); err != nil {
					return err
				}
				tx, err = db.BeginTx(ctx, nil)
				if err != nil {
					return err
				}
				if progress != nil && time.Since(lastProgress) >= time.Second {
					lastProgress = time.Now()
					fmt.Fprintf(progress, "\rImported %s events, %s calls (%s)", count(events), count(calls), time.Since(started).Round(time.Second))
				}
			}
		case "functions":
			name, file, ln := parseFunction(line)
			if _, err = tx.Exec("INSERT INTO functions VALUES(?,?,?,?)", functions, name, file, ln); err != nil {
				return err
			}
			functions++
		default:
			return fmt.Errorf("line %d: expected [events]", lineNo)
		}
	}
	if err = scanner.Err(); err != nil {
		return err
	}
	if !hadEvents || !hadFunctions {
		return fmt.Errorf("missing events or functions section")
	}
	if err = flush(); err != nil {
		return err
	}
	var missing int
	if err = tx.QueryRow("SELECT count(*) FROM nodes n LEFT JOIN functions f ON f.id=n.function_id WHERE f.id IS NULL").Scan(&missing); err != nil {
		return err
	}
	if missing > 0 {
		return fmt.Errorf("%d nodes reference missing functions", missing)
	}
	for i := range m.Metrics {
		_, err = tx.Exec(fmt.Sprintf("UPDATE nodes SET inc_%d=(SELECT COALESCE(SUM(inc_%d),0) FROM nodes WHERE parent_id=1) WHERE id=1", i, i))
		if err != nil {
			return err
		}
	}
	for k, v := range map[string]string{"calls": fmt.Sprint(calls), "events": fmt.Sprint(events), "incomplete_frames": fmt.Sprint(incomplete), "expected_calls": fmt.Sprint(m.Calls)} {
		if _, err = tx.Exec("INSERT INTO info VALUES(?,?)", k, v); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("UPDATE nodes SET calls=? WHERE id=1", calls); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	for i := range m.Metrics {
		for _, sort := range []string{"inc", "exc"} {
			if _, err = db.Exec(fmt.Sprintf("CREATE INDEX nodes_%s_%d ON nodes(parent_id,%s_%d DESC,id)", sort, i, sort, i)); err != nil {
				return err
			}
		}
	}
	if _, err = db.Exec("CREATE INDEX nodes_calls ON nodes(parent_id,calls DESC,id); CREATE INDEX nodes_function ON nodes(function_id); ANALYZE;"); err != nil {
		return err
	}
	if err = db.Close(); err != nil {
		return err
	}
	// Hard-link publication refuses to overwrite an existing destination, including races.
	if err = os.Link(tmp, out); err != nil {
		return err
	}
	if progress != nil {
		fmt.Fprintf(progress, "\rImported %s calls into %s (%s).\n", count(calls), out, time.Since(started).Round(time.Millisecond))
		if incomplete > 0 {
			fmt.Fprintf(progress, "Warning: closed %d incomplete frames at the final event.\n", incomplete)
		}
		if m.Calls != 0 && m.Calls != calls {
			fmt.Fprintf(progress, "Warning: metadata expected %d recorded calls; parsed %d.\n", m.Calls, calls)
		}
	}
	return nil
}
func count(n int64) string {
	if n >= 1000000 {
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}
