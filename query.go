package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

type reportDB struct {
	db      *sql.DB
	metrics []string
	metric  int
	sort    string
}
type node struct {
	id, parent, fid, calls int64
	inc, exc               float64
	name                   string
	children               int
}

func openReport(path string) (*reportDB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: abs, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	r := &reportDB{db: db, sort: "inc"}
	var schema, ms string
	err = db.QueryRow("SELECT value FROM info WHERE key='schema'").Scan(&schema)
	if err == nil && schema != "1" && schema != "2" {
		err = fmt.Errorf("unsupported schema %q", schema)
	}
	if err == nil {
		err = db.QueryRow("SELECT value FROM info WHERE key='metrics'").Scan(&ms)
	}
	if err == nil {
		err = json.Unmarshal([]byte(ms), &r.metrics)
	}
	if err != nil || len(r.metrics) == 0 {
		db.Close()
		if err == nil {
			err = fmt.Errorf("database has no metrics")
		}
		return nil, err
	}
	return r, nil
}
func (r *reportDB) setMetric(m string) error {
	if m == "" {
		m = r.metrics[0]
	}
	for i, v := range r.metrics {
		if v == m {
			r.metric = i
			return nil
		}
	}
	return fmt.Errorf("unknown metric %q; available: %s", m, strings.Join(r.metrics, ", "))
}
func (r *reportDB) columns() string {
	return fmt.Sprintf("n.id,n.parent_id,n.function_id,n.calls,n.inc_%d,n.exc_%d,f.name,(SELECT count(*) FROM nodes c WHERE c.parent_id=n.id)", r.metric, r.metric)
}
func (r *reportDB) order() string {
	switch r.sort {
	case "calls":
		return "n.calls DESC,n.id"
	case "exc":
		return fmt.Sprintf("n.exc_%d DESC,n.id", r.metric)
	default:
		return fmt.Sprintf("n.inc_%d DESC,n.id", r.metric)
	}
}
func scanNode(s interface{ Scan(...any) error }) (n node, e error) {
	e = s.Scan(&n.id, &n.parent, &n.fid, &n.calls, &n.inc, &n.exc, &n.name, &n.children)
	return
}
func (r *reportDB) get(id int64) (node, error) {
	return scanNode(r.db.QueryRow("SELECT "+r.columns()+" FROM nodes n JOIN functions f ON f.id=n.function_id WHERE n.id=?", id))
}
func (r *reportDB) children(parent int64, offset, limit int, showExclusive ...bool) ([]node, int, float64, error) {
	var ns []node
	rows, err := r.db.Query("SELECT "+r.columns()+" FROM nodes n JOIN functions f ON f.id=n.function_id WHERE n.parent_id=? ORDER BY "+r.order()+" LIMIT ? OFFSET ?", parent, limit, offset)
	if err != nil {
		return nil, 0, 0, err
	}
	for rows.Next() {
		n, e := scanNode(rows)
		if e != nil {
			rows.Close()
			return nil, 0, 0, e
		}
		ns = append(ns, n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, 0, err
	}
	var hidden int
	var sum float64
	col := fmt.Sprintf("inc_%d", r.metric)
	if len(showExclusive) > 0 && showExclusive[0] {
		col = fmt.Sprintf("exc_%d", r.metric)
	}
	err = r.db.QueryRow("SELECT count(*),COALESCE(SUM(value),0) FROM (SELECT n."+col+" AS value FROM nodes n WHERE parent_id=? ORDER BY "+r.order()+" LIMIT -1 OFFSET ?)", parent, offset+len(ns)).Scan(&hidden, &sum)
	return ns, hidden, sum, err
}
func (r *reportDB) search(pattern string, limit int) ([]node, error) {
	if !strings.ContainsAny(pattern, "*?[") {
		pattern = "*" + pattern + "*"
	}
	rows, err := r.db.Query("SELECT "+r.columns()+" FROM nodes n JOIN functions f ON f.id=n.function_id WHERE n.id<>1 AND f.name GLOB ? ORDER BY "+r.order()+" LIMIT ?", pattern, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ns []node
	for rows.Next() {
		n, e := scanNode(rows)
		if e != nil {
			return nil, e
		}
		ns = append(ns, n)
	}
	return ns, rows.Err()
}
func (r *reportDB) flat(pattern string, callers bool, limit int) ([]node, error) {
	where := "n.id<>1"
	args := []any{}
	join := ""
	group := "n.function_id"
	name := "f.name"
	id := "n.function_id"
	if callers {
		join = " JOIN nodes p ON p.id=n.parent_id JOIN functions pf ON pf.id=p.function_id"
		where += " AND f.name GLOB ?"
		args = append(args, pattern)
		group = "p.function_id"
		name = "pf.name"
		id = "p.function_id"
	}
	// Flat inclusive counts only outermost occurrences of each function; callers sum edges.
	inclusive := fmt.Sprintf("SUM(n.flat_%d)", r.metric)
	if callers {
		inclusive = fmt.Sprintf("SUM(n.inc_%d)", r.metric)
	}
	order := "inclusive"
	if r.sort == "exc" {
		order = "exclusive"
	}
	if r.sort == "calls" {
		order = "calls"
	}
	q := fmt.Sprintf("SELECT %s,%s,SUM(n.calls) AS calls,%s AS inclusive,SUM(n.exc_%d) AS exclusive FROM nodes n JOIN functions f ON f.id=n.function_id%s WHERE %s GROUP BY %s ORDER BY %s DESC,%s LIMIT ?", id, name, inclusive, r.metric, join, where, group, order, id)
	args = append(args, limit)
	rows, err := r.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ns []node
	for rows.Next() {
		var n node
		if err = rows.Scan(&n.id, &n.name, &n.calls, &n.inc, &n.exc); err != nil {
			return nil, err
		}
		ns = append(ns, n)
	}
	return ns, rows.Err()
}
func decimalValue(v float64) string {
	s := strings.TrimRight(strings.TrimRight(strconv.FormatFloat(v, 'f', 4, 64), "0"), ".")
	if s == "-0" {
		return "0"
	}
	return s
}
func formatValue(v float64, metric string) string {
	switch metric {
	case "wt", "ct", "it":
		// SPX reports cumulative time in microseconds (metadata divides by 1000).
		a := float64(v)
		if a >= 1e6 || a <= -1e6 {
			return fmt.Sprintf("%.3fs", a/1e6)
		}
		if a >= 1e3 || a <= -1e3 {
			return fmt.Sprintf("%.2fms", a/1e3)
		}
		return decimalValue(v) + "µs"
	case "zm", "zmab", "zmfb", "mor", "rss", "io", "ior", "iow":
		if v >= 1048576 || v <= -1048576 {
			return fmt.Sprintf("%.2fMiB", float64(v)/1048576)
		}
		if v >= 1024 || v <= -1024 {
			return fmt.Sprintf("%.2fKiB", float64(v)/1024)
		}
		return decimalValue(v) + "B"
	default:
		return decimalValue(v)
	}
}
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, s)
}
