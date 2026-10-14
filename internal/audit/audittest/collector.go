// Package audittest is an in-process OTLP/HTTP logs collector for tests.
package audittest

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/protobuf/proto"
)

// Collector records every log record it receives.
type Collector struct {
	*httptest.Server

	mu      sync.Mutex
	records []*logspb.LogRecord
	headers []http.Header
	fail    bool
}

// New starts a collector that is closed when the test ends.
func New(t testing.TB) *Collector {
	c := &Collector{}
	c.Server = httptest.NewServer(http.HandlerFunc(c.serve))
	t.Cleanup(c.Close)
	return c
}

func (c *Collector) serve(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail {
		http.Error(w, "rejected by test", http.StatusBadRequest)
		return
	}
	var body io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body = gz
	}
	raw, err := io.ReadAll(io.LimitReader(body, 8<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req collogs.ExportLogsServiceRequest
	if err := proto.Unmarshal(raw, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c.headers = append(c.headers, r.Header.Clone())
	for _, rl := range req.ResourceLogs {
		for _, sl := range rl.ScopeLogs {
			c.records = append(c.records, sl.LogRecords...)
		}
	}
	out, _ := proto.Marshal(&collogs.ExportLogsServiceResponse{})
	w.Header().Set("Content-Type", "application/x-protobuf")
	_, _ = w.Write(out)
}

// Fail makes the collector reject every export.
func (c *Collector) Fail() {
	c.mu.Lock()
	c.fail = true
	c.mu.Unlock()
}

// Events returns the records with the given event name, in arrival order.
func (c *Collector) Events(name string) []*logspb.LogRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*logspb.LogRecord
	for _, r := range c.records {
		if r.EventName == name {
			out = append(out, r)
		}
	}
	return out
}

// Headers returns the request headers of every export received.
func (c *Collector) Headers() []http.Header {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]http.Header(nil), c.headers...)
}

// Attr returns a record attribute rendered as a string ("" if absent).
func Attr(r *logspb.LogRecord, key string) string {
	for _, kv := range r.Attributes {
		if kv.Key != key {
			continue
		}
		switch v := kv.Value.Value.(type) {
		case *commonpb.AnyValue_StringValue:
			return v.StringValue
		case *commonpb.AnyValue_IntValue:
			return strconv.FormatInt(v.IntValue, 10)
		case *commonpb.AnyValue_BoolValue:
			return strconv.FormatBool(v.BoolValue)
		}
	}
	return ""
}

// Body returns a record's string body.
func Body(r *logspb.LogRecord) string { return r.Body.GetStringValue() }
