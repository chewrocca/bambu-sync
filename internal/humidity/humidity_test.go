package humidity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Regression: prometheus/common v0.70.x TextParser carries an UNEXPORTED
// scheme field whose zero value is model.UnsetValidation, and parsing panics
// on the first metric line. A zero-value &expfmt.TextParser{} is therefore
// broken by construction — it must be built with NewTextParser.
//
// This shipped, and CrashLoopBackOffed on the first real deploy. The panic was
// in a ticker goroutine, so it took the whole process down rather than
// degrading an explicitly best-effort reading.
func TestReadParsesRealExposition(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		w.Write([]byte("# HELP bambulab_ams_unit_humidity AMS humidity\n# TYPE bambulab_ams_unit_humidity gauge\nbambulab_ams_unit_humidity{printer_name=\"bambulab\",ams_id=\"0\"} 36\n"))
	}))
	defer srv.Close()
	r, err := New(srv.URL, "bambulab_ams_unit_humidity").Read(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	t.Logf("OK percent=%v available=%v", r.Percent, r.Available)
}

// The read is documented as best-effort. Prove that survives a malformed body
// rather than only a well-formed one — the failure mode that caused the outage
// was an optional reading escalating into a process-level crash.
func TestReadDegradesRatherThanCrashing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("this is not exposition format {{{\n"))
	}))
	defer srv.Close()

	r, err := New(srv.URL, "bambulab_ams_unit_humidity").Read(context.Background())
	if err == nil {
		t.Error("want an error for a malformed body")
	}
	if r.Available {
		t.Error("a failed read must not report a reading as available")
	}
}

// An empty URL disables the read entirely — correct outside a cluster, and it
// must not be an error.
func TestReadDisabledWhenURLEmpty(t *testing.T) {
	r, err := New("", "x").Read(context.Background())
	if err != nil || r.Available {
		t.Errorf("want silent no-op, got r=%+v err=%v", r, err)
	}
}
