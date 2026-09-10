package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

type event struct {
	fid    int64
	start  bool
	values []float64
}
type eventParser struct {
	recent  []event
	metrics int
	format  int
}

func number(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }

// SPX writes metrics as doubles, including fractional noise-correction deltas.
// IDs remain strict integers.
func metricNumber(s string) (float64, error) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("non-finite metric %q", s)
	}
	return v, nil
}
func (p *eventParser) parse(line string) (event, error) {
	var e event
	legacy := strings.ContainsAny(line, " \t")
	format := 2
	if legacy {
		format = 1
	}
	if p.format != 0 && p.format != format {
		return e, fmt.Errorf("mixed event encodings")
	}
	p.format = format
	if legacy {
		f := strings.Fields(line)
		if len(f) != p.metrics+2 {
			return e, fmt.Errorf("expected %d event columns, got %d", p.metrics+2, len(f))
		}
		var err error
		e.fid, err = number(f[0])
		if err != nil {
			return e, err
		}
		if f[1] != "0" && f[1] != "1" {
			return e, fmt.Errorf("invalid event type %q", f[1])
		}
		e.start = f[1] == "1"
		e.values = make([]float64, p.metrics)
		for i := range e.values {
			e.values[i], err = metricNumber(f[i+2])
			if err != nil {
				return e, err
			}
		}
	} else {
		f := strings.Split(line, "|")
		e.start = !strings.HasPrefix(line, "-")
		off := 0
		if e.start {
			off = 1
		}
		if len(f) != p.metrics+1+off {
			return e, fmt.Errorf("invalid compressed event column count")
		}
		if e.start {
			if _, err := strconv.ParseUint(f[0], 16, 64); err != nil && f[0] != "" {
				return e, fmt.Errorf("invalid call site: %w", err)
			}
		}
		id := f[off]
		if !e.start {
			id = strings.TrimPrefix(id, "-")
		}
		var err error
		if strings.HasPrefix(id, "r") {
			n, err := strconv.Atoi(id[1:])
			if err != nil || n < 1 || n > len(p.recent) {
				return e, fmt.Errorf("invalid back-reference %q", id)
			}
			e.fid = p.recent[n-1].fid
		} else {
			e.fid, err = number(id)
			if err != nil {
				return e, err
			}
		}
		e.values = make([]float64, p.metrics)
		for i := range e.values {
			v := f[i+off+1]
			absolute := strings.HasPrefix(v, "a")
			if absolute {
				v = v[1:]
			}
			if v == "" {
				v = "0"
			}
			n, err := metricNumber(v)
			if err != nil {
				return e, err
			}
			if !absolute && len(p.recent) > 0 {
				n += p.recent[0].values[i]
				if math.IsInf(n, 0) {
					return e, fmt.Errorf("metric delta overflow")
				}
			}
			e.values[i] = n
		}
		p.recent = append([]event{e}, p.recent...)
		if len(p.recent) > 20 {
			p.recent = p.recent[:20]
		}
	}
	if e.fid < 0 {
		return e, fmt.Errorf("negative function id")
	}
	return e, nil
}
func parseFunction(s string) (string, string, int) {
	parts := strings.Split(s, ":")
	for i := 1; i < len(parts)-1; i++ {
		n, err := strconv.Atoi(parts[i])
		if err == nil && n >= 0 {
			file := strings.Join(parts[:i], ":")
			name := strings.Join(parts[i+1:], ":")
			if name == "{closure}" {
				name = fmt.Sprintf("{closure:%s:%d}", file, n)
			}
			return name, file, n
		}
	}
	return s, "", 0
}
