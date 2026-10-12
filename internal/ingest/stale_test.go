package ingest

import (
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/tynext/tynext-poc/internal/contract"
)

func testStale(t *testing.T, start time.Time) *staleDetector {
	t.Helper()
	devices := map[string]deviceRef{
		contract.PublisherID: {id: "11111111-1111-1111-1111-111111111111", class: contract.ClassEMS},
		"pm-04":              {id: "22222222-2222-2222-2222-222222222222", class: contract.ClassMeter},
		"qf-5":               {id: "33333333-3333-3333-3333-333333333333", class: contract.ClassBreaker},
	}
	cfg := Config{StaleAfter: DefaultStaleAfter, StaleCheckEvery: DefaultStaleCheckEvery}
	return newStaleDetector(cfg, nil, zerolog.Nop(), NewMetrics(), devices, start)
}

func actionFor(actions []staleAction, code string) (staleAction, bool) {
	for _, a := range actions {
		if a.code == code {
			return a, true
		}
	}
	return staleAction{}, false
}

func TestStaleRaisesOnlyAfterTheGap(t *testing.T) {
	start := time.Date(2026, 10, 12, 4, 30, 0, 0, time.UTC)
	s := testStale(t, start)

	// Within the window nothing fires, even though no telemetry has arrived.
	if actions, open := s.decide(start.Add(4 * time.Second)); len(actions) != 0 || open != 0 {
		t.Fatalf("4 s after start: actions=%d open=%d, want none", len(actions), open)
	}

	actions, open := s.decide(start.Add(6 * time.Second))
	if len(actions) != 3 || open != 3 {
		t.Fatalf("6 s after start: actions=%d open=%d, want 3 and 3", len(actions), open)
	}
	for _, a := range actions {
		if !a.raise {
			t.Errorf("%s: expected a raise", a.code)
		}
	}

	// A second sweep must not raise again — the alarm is already open.
	if again, _ := s.decide(start.Add(7 * time.Second)); len(again) != 0 {
		t.Errorf("second sweep produced %d more actions, want 0", len(again))
	}
}

// The EMS-PV controller is the only publisher, so its silence is critical while
// any other device going quiet is a warning.
func TestStaleSeverityIsCriticalForThePublisher(t *testing.T) {
	start := time.Date(2026, 10, 12, 4, 30, 0, 0, time.UTC)
	s := testStale(t, start)

	actions, _ := s.decide(start.Add(6 * time.Second))

	ems, ok := actionFor(actions, contract.PublisherID)
	if !ok {
		t.Fatalf("no action for %s", contract.PublisherID)
	}
	if ems.severity != contract.SeverityCritical {
		t.Errorf("%s severity = %q, want critical", contract.PublisherID, ems.severity)
	}

	meter, ok := actionFor(actions, "pm-04")
	if !ok {
		t.Fatal("no action for pm-04")
	}
	if meter.severity != contract.SeverityWarning {
		t.Errorf("pm-04 severity = %q, want warning", meter.severity)
	}
}

func TestStaleClearsWhenDataComesBack(t *testing.T) {
	start := time.Date(2026, 10, 12, 4, 30, 0, 0, time.UTC)
	s := testStale(t, start)

	s.decide(start.Add(6 * time.Second)) // everything goes stale

	s.seen("pm-04", start.Add(10*time.Second))
	actions, open := s.decide(start.Add(11 * time.Second))

	if len(actions) != 1 {
		t.Fatalf("got %d actions, want 1", len(actions))
	}
	if actions[0].code != "pm-04" || actions[0].raise {
		t.Errorf("action = %+v, want a clear for pm-04", actions[0])
	}
	if open != 2 {
		t.Errorf("open = %d, want 2 (the other two devices are still quiet)", open)
	}

	// And it can go stale again once it stops.
	actions, _ = s.decide(start.Add(17 * time.Second))
	if a, ok := actionFor(actions, "pm-04"); !ok || !a.raise {
		t.Error("pm-04 should go stale again after it stops reporting")
	}
}

// A device reporting every second never trips the detector.
func TestStaleStaysQuietWhileTelemetryFlows(t *testing.T) {
	start := time.Date(2026, 10, 12, 4, 30, 0, 0, time.UTC)
	s := testStale(t, start)

	for i := 1; i <= 30; i++ {
		at := start.Add(time.Duration(i) * time.Second)
		for code := range s.devices {
			s.seen(code, at)
		}
		if actions, open := s.decide(at); len(actions) != 0 || open != 0 {
			t.Fatalf("second %d: actions=%d open=%d, want none", i, len(actions), open)
		}
	}
}

// A failed write must leave the flag so the next sweep retries, instead of the
// detector believing it already did the work.
func TestStaleFailedRetriesNextSweep(t *testing.T) {
	start := time.Date(2026, 10, 12, 4, 30, 0, 0, time.UTC)
	s := testStale(t, start)

	actions, _ := s.decide(start.Add(6 * time.Second))
	if len(actions) != 3 {
		t.Fatalf("setup: got %d actions", len(actions))
	}

	// The raise for pm-04 did not reach the database.
	s.failed(errFake, "pm-04", false)
	retry, _ := s.decide(start.Add(7 * time.Second))
	if a, ok := actionFor(retry, "pm-04"); !ok || !a.raise {
		t.Error("a failed raise should be retried on the next sweep")
	}

	// Now the clear fails.
	s.seen("pm-04", start.Add(8*time.Second))
	if _, open := s.decide(start.Add(9 * time.Second)); open != 2 {
		t.Fatalf("setup: open = %d, want 2", open)
	}
	s.failed(errFake, "pm-04", true)
	retry, _ = s.decide(start.Add(10 * time.Second))
	if a, ok := actionFor(retry, "pm-04"); !ok || a.raise {
		t.Error("a failed clear should be retried on the next sweep")
	}
}

var errFake = errFakeType("tulis gagal")

type errFakeType string

func (e errFakeType) Error() string { return string(e) }
