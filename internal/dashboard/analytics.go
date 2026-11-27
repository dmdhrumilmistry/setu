package dashboard

import (
	"sort"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/history"
	"github.com/dmdhrumilmistry/setu/internal/proto"
)

type rangeSpec struct {
	key    string
	span   time.Duration
	hourly bool // hour buckets; otherwise local calendar days
	n      int  // number of buckets
}

var ranges = map[string]rangeSpec{
	"24h": {key: "24h", span: 24 * time.Hour, hourly: true, n: 24},
	"7d":  {key: "7d", span: 7 * 24 * time.Hour, n: 7},
	"30d": {key: "30d", span: 30 * 24 * time.Hour, n: 30},
	"90d": {key: "90d", span: 90 * 24 * time.Hour, n: 90},
}

type analyticsView struct {
	Range   string          `json:"range"`
	From    time.Time       `json:"from"`
	To      time.Time       `json:"to"`
	Hourly  bool            `json:"hourly"`
	Totals  totals          `json:"totals"`
	Series  []bucket        `json:"series"`
	Hours   [24]int         `json:"hours"` // connections by local hour of day
	Who     []whoRow        `json:"who"`
	Sources []sourceRow     `json:"sources"`
	Events  []history.Event `json:"events"` // newest first
}

type totals struct {
	Connections int `json:"connections"`
	// Previous is Connections over the same span just before, for a trend.
	Previous      int     `json:"previous"`
	Reconnects    int     `json:"reconnects"`
	People        int     `json:"people"`  // distinct personal keys
	Devices       int     `json:"devices"` // distinct device keys
	Control       int     `json:"control"`
	View          int     `json:"view"`
	Seconds       float64 `json:"seconds"`        // time connected, all connections
	MedianSeconds float64 `json:"median_seconds"` // of one connection
	AuthFailures  int     `json:"auth_failures"`
	Lockouts      int     `json:"lockouts"`
	Rejected      int     `json:"rejected"`
	Paired        int     `json:"paired"`
}

type bucket struct {
	Start    time.Time `json:"start"`
	Control  int       `json:"control"`
	View     int       `json:"view"`
	Failures int       `json:"failures"`
}

type whoRow struct {
	Name        string    `json:"name"`
	Personal    bool      `json:"personal"`
	Connections int       `json:"connections"`
	Seconds     float64   `json:"seconds"`
	LastSeen    time.Time `json:"last_seen"`
}

type sourceRow struct {
	Source      string `json:"source"`
	Connections int    `json:"connections"`
}

const maxEvents = 60

// shared are the actor names that do not identify one person.
var shared = map[string]bool{"shared-control-link": true, "shared-view-link": true, "host-terminal": true}

// analyze turns history into what the overview shows. evs covers twice the
// range, so the previous period can be compared.
func analyze(evs []history.Event, rng rangeSpec, now time.Time) analyticsView {
	now = now.Local()
	var first time.Time
	if rng.hourly {
		first = now.Truncate(time.Hour).Add(-time.Duration(rng.n-1) * time.Hour)
	} else {
		y, m, d := now.Date()
		first = time.Date(y, m, d-(rng.n-1), 0, 0, 0, 0, time.Local)
	}
	start := func(i int) time.Time {
		if rng.hourly {
			return first.Add(time.Duration(i) * time.Hour)
		}
		y, m, d := first.Date()
		return time.Date(y, m, d+i, 0, 0, 0, 0, time.Local)
	}
	index := func(t time.Time) int {
		if t.Before(first) || t.After(now) {
			return -1
		}
		if rng.hourly {
			return int(t.Sub(first) / time.Hour)
		}
		y, m, d := t.Date()
		fy, fm, fd := first.Date()
		days := int(time.Date(y, m, d, 12, 0, 0, 0, time.UTC).Sub(time.Date(fy, fm, fd, 12, 0, 0, 0, time.UTC)).Hours() / 24)
		if days >= rng.n {
			return -1
		}
		return days
	}
	prevFrom := first.Add(-now.Sub(first))

	v := analyticsView{Range: rng.key, From: first, To: now, Hourly: rng.hourly, Series: make([]bucket, rng.n), Who: []whoRow{}, Sources: []sourceRow{}, Events: []history.Event{}}
	for i := range v.Series {
		v.Series[i].Start = start(i)
	}
	who := map[string]*whoRow{}
	src := map[string]int{}
	people, devices := map[string]bool{}, map[string]bool{}
	var visits []float64
	for _, e := range evs {
		t := e.Time.Local()
		i := index(t)
		if i < 0 {
			if e.Kind == history.Connect && !t.Before(prevFrom) && t.Before(first) {
				v.Totals.Previous++
			}
			continue
		}
		b := &v.Series[i]
		switch e.Kind {
		case history.Connect:
			v.Totals.Connections++
			if e.Role == proto.RoleView {
				b.View++
				v.Totals.View++
			} else {
				b.Control++
				v.Totals.Control++
			}
			v.Hours[t.Hour()]++
			src[e.Source]++
			if e.DeviceKey != "" {
				devices[e.DeviceKey] = true
			}
			if e.Name != "" && !shared[e.Name] {
				people[e.Name] = true
			}
			w := who[e.Name]
			if w == nil {
				w = &whoRow{Name: e.Name, Personal: !shared[e.Name]}
				who[e.Name] = w
			}
			w.Connections++
			if t.After(w.LastSeen) {
				w.LastSeen = t
			}
		case history.Reconnect:
			v.Totals.Reconnects++
		case history.Disconnect:
			v.Totals.Seconds += e.Seconds
			visits = append(visits, e.Seconds)
			if w := who[e.Name]; w != nil {
				w.Seconds += e.Seconds
			}
		case history.AuthFailure:
			v.Totals.AuthFailures++
			b.Failures++
		case history.Lockout:
			v.Totals.Lockouts++
		case history.Rejected:
			v.Totals.Rejected++
		case history.Paired:
			v.Totals.Paired++
		}
		if e.Kind != history.SessionStart && e.Kind != history.SessionEnd {
			v.Events = append(v.Events, e)
		}
	}
	v.Totals.People, v.Totals.Devices = len(people), len(devices)
	if len(visits) > 0 {
		sort.Float64s(visits)
		mid := len(visits) / 2
		v.Totals.MedianSeconds = visits[mid]
		if len(visits)%2 == 0 {
			v.Totals.MedianSeconds = (visits[mid-1] + visits[mid]) / 2
		}
	}
	for _, w := range who {
		v.Who = append(v.Who, *w)
	}
	sort.Slice(v.Who, func(i, j int) bool {
		if v.Who[i].Connections != v.Who[j].Connections {
			return v.Who[i].Connections > v.Who[j].Connections
		}
		return v.Who[i].Name < v.Who[j].Name
	})
	if len(v.Who) > 8 {
		v.Who = v.Who[:8]
	}
	for s, n := range src {
		v.Sources = append(v.Sources, sourceRow{Source: s, Connections: n})
	}
	sort.Slice(v.Sources, func(i, j int) bool {
		if v.Sources[i].Connections != v.Sources[j].Connections {
			return v.Sources[i].Connections > v.Sources[j].Connections
		}
		return v.Sources[i].Source < v.Sources[j].Source
	})
	// Newest first, bounded.
	for i, j := 0, len(v.Events)-1; i < j; i, j = i+1, j-1 {
		v.Events[i], v.Events[j] = v.Events[j], v.Events[i]
	}
	if len(v.Events) > maxEvents {
		v.Events = v.Events[:maxEvents]
	}
	return v
}
