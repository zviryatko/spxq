package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/mattn/go-runewidth"
)

type uiTheme struct {
	light          bool
	normal, accent tcell.Style
}

func newTheme(name string) (*uiTheme, error) {
	if name != "dark" && name != "light" {
		return nil, fmt.Errorf("theme must be dark or light")
	}
	t := &uiTheme{light: name == "light"}
	t.update()
	return t, nil
}
func (t *uiTheme) update() {
	if t.light {
		t.normal = tcell.StyleDefault.Foreground(tcell.NewHexColor(0x1f2937)).Background(tcell.NewHexColor(0xfafafa))
		t.accent = t.normal.Foreground(tcell.NewHexColor(0x075985))
	} else {
		t.normal = tcell.StyleDefault.Foreground(tcell.ColorWhite).Background(tcell.ColorBlack)
		t.accent = t.normal.Foreground(tcell.ColorTeal)
	}
}
func (t *uiTheme) toggle(s tcell.Screen) {
	t.light = !t.light
	t.update()
	s.SetStyle(t.normal)
}

func shortBreadcrumb(name string) string {
	if class, method, ok := strings.Cut(name, "::"); ok {
		if i := strings.LastIndex(class, `\`); i >= 0 {
			class = class[i+1:]
		}
		return class + "::" + method
	}
	if strings.Contains(name, "/") || strings.Contains(name, `:\`) {
		parts := strings.Split(strings.TrimRight(strings.ReplaceAll(name, `\`, "/"), "/"), "/")
		if len(parts) > 2 {
			parts = parts[len(parts)-2:]
		}
		return strings.Join(parts, "/")
	}
	if i := strings.LastIndex(name, `\`); i >= 0 {
		return name[i+1:]
	}
	return name
}
func breadcrumbText(names []string) string {
	labels := append([]string(nil), names...)
	for i := 0; i < len(labels)-1; i++ {
		labels[i] = shortBreadcrumb(labels[i])
	}
	return strings.Join(labels, " › ")
}

func put(s tcell.Screen, x, y int, text string, style tcell.Style) {
	w, h := s.Size()
	if y < 0 || y >= h {
		return
	}
	for _, r := range clean(text) {
		rw := runewidth.RuneWidth(r)
		if x+rw > w {
			return
		}
		if x >= 0 {
			s.SetContent(x, y, r, nil, style)
		}
		x += rw
	}
}
func screen(theme *uiTheme) (tcell.Screen, error) {
	s, e := tcell.NewScreen()
	if e != nil {
		return nil, e
	}
	if e = s.Init(); e != nil {
		return nil, e
	}
	s.SetStyle(theme.normal)
	return s, nil
}
func pickReport(rs []reportEntry, theme *uiTheme) (string, error) {
	if len(rs) == 0 {
		return "", fmt.Errorf("no readable SPX reports")
	}
	s, e := screen(theme)
	if e != nil {
		return "", e
	}
	defer s.Fini()
	selected := 0
	offset := 0
	for {
		normal, accent := theme.normal, theme.accent
		s.Clear()
		w, h := s.Size()
		put(s, 0, 0, fmt.Sprintf("SPX reports · %d available · newest first", len(rs)), accent)
		put(s, 0, 1, "↑↓ select · ←→ scroll columns · Enter index/open · t theme · q quit", normal)
		widths := []int{16, 14, max(24, w/4), max(20, w/5), 8, 10, 54}
		columns := func(values []string) string {
			cells := make([]string, len(values))
			for i, v := range values {
				cells[i] = runewidth.FillRight(runewidth.Truncate(clean(v), widths[i], "…"), widths[i])
			}
			return strings.Join(cells, "  ")
		}
		heading := columns([]string{"DATE", "HOST", "COMMAND / URL", "CUSTOM METADATA", "CALLS", "SIZE (MiB)", "REPORT"})
		current := rs[selected]
		custom := current.m.Custom
		if custom == "" {
			custom = "—"
		}
		details := []string{
			"Report: " + strings.TrimSuffix(filepath.Base(current.path), ".json"),
			"Date: " + time.Unix(current.m.Timestamp, 0).Format("2006-01-02 15:04:05 MST") + " · Host: " + current.m.Host,
			"Command / URL: " + current.m.target(),
			"Custom metadata: " + custom,
		}
		contentWidth := runewidth.StringWidth(heading)
		for _, line := range details {
			contentWidth = max(contentWidth, runewidth.StringWidth(clean(line)))
		}
		offset = min(offset, max(0, contentWidth-w))
		put(s, -offset, 2, heading, accent)
		height := max(1, h-8)
		top := selected / height * height
		for i := top; i < len(rs) && i < top+height; i++ {
			r := rs[i]
			style := normal
			if i == selected {
				style = style.Reverse(true)
			}
			custom := r.m.Custom
			if custom == "" {
				custom = "—"
			}
			label := columns([]string{time.Unix(r.m.Timestamp, 0).Format("2006-01-02 15:04"), r.m.Host, r.m.target(), custom, count(r.m.Calls), fmt.Sprintf("%.1f", float64(r.size)/1048576), strings.TrimSuffix(filepath.Base(r.path), ".json")})
			put(s, -offset, 3+i-top, label, style)
		}
		for i, line := range details {
			put(s, -offset, h-4+i, line, accent)
		}
		s.Show()
		ev := s.PollEvent()
		switch ev := ev.(type) {
		case *tcell.EventResize:
			s.Sync()
		case *tcell.EventKey:
			switch ev.Key() {
			case tcell.KeyEscape, tcell.KeyCtrlC:
				return "", nil
			case tcell.KeyLeft:
				offset = max(0, offset-20)
			case tcell.KeyRight:
				offset += 20
			case tcell.KeyUp:
				if selected > 0 {
					selected--
				}
			case tcell.KeyDown:
				if selected+1 < len(rs) {
					selected++
				}
			case tcell.KeyPgDn:
				selected = min(len(rs)-1, selected+height)
			case tcell.KeyPgUp:
				selected = max(0, selected-height)
			case tcell.KeyEnter:
				return rs[selected].path, nil
			case tcell.KeyRune:
				if ev.Rune() == 't' {
					theme.toggle(s)
				}
				if ev.Rune() == 'q' {
					return "", nil
				}
			}
		}
	}
}

type displayRow struct {
	n      node
	depth  int
	more   int
	sum    float64
	parent int64
}

func view(r *reportDB, root int64, limit int, theme *uiTheme) error {
	if _, e := r.get(root); e != nil {
		return e
	}
	s, e := screen(theme)
	if e != nil {
		return e
	}
	defer s.Fini()
	expanded := map[int64]int{root: limit}
	selected, top := 0, 0
	exclusive := false
	var rows []displayRow
	var history []int64
	query := ""
	searching := false
	searchMode := false
	status := ""
	var rebuild func() error
	rebuild = func() error {
		rows = nil
		if searchMode {
			ns, e := r.search(query, 1000)
			if e != nil {
				return e
			}
			for _, n := range ns {
				rows = append(rows, displayRow{n: n})
			}
			return nil
		}
		n, e := r.get(root)
		if e != nil {
			return e
		}
		rows = append(rows, displayRow{n: n})
		var add func(int64, int) error
		add = func(id int64, d int) error {
			lim := expanded[id]
			if lim == 0 {
				return nil
			}
			ns, hidden, sum, e := r.children(id, 0, lim, exclusive)
			if e != nil {
				return e
			}
			for _, n := range ns {
				rows = append(rows, displayRow{n: n, depth: d})
				if e = add(n.id, d+1); e != nil {
					return e
				}
			}
			if hidden > 0 {
				rows = append(rows, displayRow{depth: d, more: hidden, sum: sum, parent: id})
			}
			return nil
		}
		return add(root, 1)
	}
	if e = rebuild(); e != nil {
		return e
	}
	for {
		normal, accent := theme.normal, theme.accent
		w, h := s.Size()
		height := max(1, h-6)
		if selected >= len(rows) {
			selected = max(0, len(rows)-1)
		}
		if selected < top {
			top = selected
		}
		if selected >= top+height {
			top = selected - height + 1
		}
		s.Clear()
		mode := "inclusive"
		if exclusive {
			mode = "exclusive"
		}
		put(s, 0, 0, fmt.Sprintf("SPX · %s · %s · sort %s · page %d", r.metrics[r.metric], mode, r.sort, limit), accent)
		crumb := []string{}
		id := root
		for id > 0 {
			n, e := r.get(id)
			if e != nil {
				return e
			}
			crumb = append([]string{n.name}, crumb...)
			id = n.parent
		}
		put(s, 0, 1, breadcrumbText(crumb), normal)
		if searchMode {
			put(s, 0, 1, fmt.Sprintf("Search: %s · first %d matches · Enter focus · Esc clear", query, len(rows)), accent)
		}
		put(s, 0, 2, "      VALUE    %FOCUS      CALLS   FUNCTION", accent)
		rootNode, e := r.get(root)
		if e != nil {
			return e
		}
		den := float64(rootNode.inc)
		for i := top; i < len(rows) && i < top+height; i++ {
			row := rows[i]
			style := normal
			if i == selected {
				style = style.Reverse(true)
			}
			var text string
			if row.more > 0 {
				text = fmt.Sprintf("%11s                      %s… %d more (Enter / PgDn)", formatValue(row.sum, r.metrics[r.metric]), strings.Repeat("  ", row.depth), row.more)
			} else {
				v := row.n.inc
				if exclusive {
					v = row.n.exc
				}
				pct := 0.0
				if den != 0 {
					pct = 100 * float64(v) / den
				}
				mark := " "
				if row.n.children > 0 {
					mark = "▶"
					if expanded[row.n.id] > 0 && !searchMode {
						mark = "▼"
					}
				}
				text = fmt.Sprintf("%11s %8.2f%% %10s   %s%s %s", formatValue(v, r.metrics[r.metric]), pct, count(row.n.calls), strings.Repeat("  ", min(row.depth, 100)), mark, row.n.name)
			}
			put(s, 0, 3+i-top, text, style)
		}
		put(s, 0, h-3, "↑↓ select  → expand  ← collapse  Enter focus  Backspace back  Home report  PgDn more", accent)
		put(s, 0, h-2, "/ search  m metric  i values  s sort  t theme  q quit", accent)
		if searching {
			put(s, 0, h-1, "/"+query, normal)
			s.ShowCursor(min(w-1, 1+runewidth.StringWidth(query)), h-1)
		} else {
			s.HideCursor()
			put(s, 0, h-1, status, normal)
		}
		s.Show()
		ev := s.PollEvent()
		switch ev := ev.(type) {
		case *tcell.EventResize:
			s.Sync()
			continue
		case *tcell.EventKey:
			if ev.Key() == tcell.KeyCtrlC {
				return nil
			}
			if searching {
				switch ev.Key() {
				case tcell.KeyEscape:
					searching = false
					query = ""
				case tcell.KeyEnter:
					searching = false
					searchMode = query != ""
					selected = 0
					top = 0
				case tcell.KeyBackspace, tcell.KeyBackspace2:
					rr := []rune(query)
					if len(rr) > 0 {
						query = string(rr[:len(rr)-1])
					}
				case tcell.KeyRune:
					query += string(ev.Rune())
				}
				if e = rebuild(); e != nil {
					return e
				}
				continue
			}
			var row displayRow
			if len(rows) > 0 {
				row = rows[selected]
			}
			status = ""
			focus := func(id int64) {
				history = append(history, root)
				root = id
				expanded = map[int64]int{root: limit}
				selected = 0
				top = 0
				searchMode = false
			}
			switch ev.Key() {
			case tcell.KeyEscape:
				searchMode = false
				selected = 0
				top = 0
			case tcell.KeyUp:
				selected = max(0, selected-1)
			case tcell.KeyDown:
				selected = min(len(rows)-1, selected+1)
			case tcell.KeyPgUp:
				selected = max(0, selected-height)
			case tcell.KeyPgDn:
				if row.more > 0 {
					expanded[row.parent] += limit
				} else if row.n.children > 0 && !searchMode {
					expanded[row.n.id] += limit
				} else {
					selected = min(len(rows)-1, selected+height)
				}
			case tcell.KeyRight:
				if row.more > 0 {
					expanded[row.parent] += limit
				} else if row.n.children > 0 {
					if searchMode {
						focus(row.n.id)
					} else if expanded[row.n.id] == 0 {
						expanded[row.n.id] = limit
					} else {
						selected = min(len(rows)-1, selected+1)
					}
				}
			case tcell.KeyLeft:
				if row.more == 0 && expanded[row.n.id] > 0 {
					delete(expanded, row.n.id)
				} else {
					parent := row.n.parent
					if row.more > 0 {
						parent = row.parent
					}
					for i, n := range rows {
						if n.more == 0 && n.n.id == parent {
							selected = i
							break
						}
					}
				}
			case tcell.KeyEnter:
				if row.more > 0 {
					expanded[row.parent] += limit
				} else if row.n.id != 0 {
					focus(row.n.id)
				}
			case tcell.KeyBackspace, tcell.KeyBackspace2:
				if len(history) > 0 {
					root = history[len(history)-1]
					history = history[:len(history)-1]
					expanded = map[int64]int{root: limit}
					selected = 0
					top = 0
					searchMode = false
				} else if root != 1 {
					n, e := r.get(root)
					if e != nil {
						return e
					}
					if n.parent > 0 {
						focus(n.parent)
					}
				}
			case tcell.KeyHome:
				focus(1)
			case tcell.KeyRune:
				switch ev.Rune() {
				case 't':
					theme.toggle(s)
				case 'q':
					return nil
				case '/':
					searching = true
					query = ""
				case 'm':
					r.metric = (r.metric + 1) % len(r.metrics)
				case 'i':
					exclusive = !exclusive
				case 's':
					switch r.sort {
					case "inc":
						r.sort = "exc"
					case "exc":
						r.sort = "calls"
					default:
						r.sort = "inc"
					}
				}
			}
			if e = rebuild(); e != nil {
				return e
			}
		}
	}
}
