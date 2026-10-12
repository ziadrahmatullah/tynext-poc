package sim

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func newTestQueue(t *testing.T, maxAge time.Duration) *Queue {
	t.Helper()
	q, err := OpenQueue(filepath.Join(t.TempDir(), "sim.queue"), maxAge)
	if err != nil {
		t.Fatalf("OpenQueue: %v", err)
	}
	t.Cleanup(func() {
		if err := q.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return q
}

func entry(i int, ts time.Time) Entry {
	return Entry{
		Topic:   "rain/pts/meter/pm-04/telemetry",
		Payload: []byte(fmt.Sprintf(`{"seq":%d}`, i)),
		QoS:     1,
		TS:      ts,
	}
}

// Replay has to preserve order, or the database would see the plant's history
// out of sequence.
func TestReplayKeepsOrder(t *testing.T) {
	q := newTestQueue(t, time.Hour)
	now := time.Now().UTC()

	for i := 0; i < 100; i++ {
		if err := q.Append(entry(i, now.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	if got := q.Pending(); got != 100 {
		t.Fatalf("Pending = %d, want 100", got)
	}

	var seen []string
	result := q.Replay(now.Add(2*time.Minute), func(e Entry) error {
		seen = append(seen, string(e.Payload))
		return nil
	})
	if result.Err != nil {
		t.Fatalf("Replay: %v", result.Err)
	}
	if result.Sent != 100 {
		t.Errorf("Sent = %d, want 100", result.Sent)
	}
	for i, payload := range seen {
		if want := fmt.Sprintf(`{"seq":%d}`, i); payload != want {
			t.Fatalf("entry %d = %s, want %s — order was not preserved", i, payload, want)
		}
	}
	if got := q.Pending(); got != 0 {
		t.Errorf("Pending = %d after a full replay, want 0", got)
	}
}

// The original ts travels with the message, so the gap is filled with the time
// the plant measured, not the time we caught up.
func TestReplayCarriesTheOriginalTimestamp(t *testing.T) {
	q := newTestQueue(t, time.Hour)
	measured := time.Date(2026, 10, 12, 4, 30, 0, 0, time.UTC)

	if err := q.Append(entry(1, measured)); err != nil {
		t.Fatalf("Append: %v", err)
	}

	var got time.Time
	result := q.Replay(measured.Add(10*time.Minute), func(e Entry) error {
		got = e.TS
		return nil
	})
	if result.Err != nil {
		t.Fatalf("Replay: %v", result.Err)
	}
	if !got.Equal(measured) {
		t.Errorf("ts = %s, want the original %s", got, measured)
	}
}

// A failed publish must stop the pass and keep the position, so the next pass
// continues instead of resending everything.
func TestReplayStopsAtFirstFailureAndResumes(t *testing.T) {
	q := newTestQueue(t, time.Hour)
	now := time.Now().UTC()
	for i := 0; i < 10; i++ {
		if err := q.Append(entry(i, now)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	boom := errors.New("broker pergi")
	sent := 0
	result := q.Replay(now, func(Entry) error {
		if sent == 4 {
			return boom
		}
		sent++
		return nil
	})
	if !errors.Is(result.Err, boom) {
		t.Fatalf("Err = %v, want the publish error", result.Err)
	}
	if result.Sent != 4 {
		t.Errorf("Sent = %d, want 4", result.Sent)
	}
	if got := q.Pending(); got != 6 {
		t.Errorf("Pending = %d, want 6", got)
	}

	var rest []string
	result = q.Replay(now, func(e Entry) error {
		rest = append(rest, string(e.Payload))
		return nil
	})
	if result.Err != nil {
		t.Fatalf("second pass: %v", result.Err)
	}
	if len(rest) != 6 {
		t.Fatalf("second pass sent %d, want the remaining 6", len(rest))
	}
	if rest[0] != `{"seq":4}` {
		t.Errorf("second pass resumed at %s, want {\"seq\":4} — nothing should be resent", rest[0])
	}
}

// Anything older than the buffer window is dropped rather than replayed.
func TestReplayDropsExpiredEntries(t *testing.T) {
	q := newTestQueue(t, time.Hour)
	now := time.Now().UTC()

	if err := q.Append(entry(0, now.Add(-3*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := q.Append(entry(1, now.Add(-30*time.Minute))); err != nil {
		t.Fatal(err)
	}

	var sent int
	result := q.Replay(now, func(Entry) error {
		sent++
		return nil
	})
	if result.Err != nil {
		t.Fatalf("Replay: %v", result.Err)
	}
	if result.Expired != 1 {
		t.Errorf("Expired = %d, want 1", result.Expired)
	}
	if sent != 1 {
		t.Errorf("sent %d entries, want 1", sent)
	}
}

// The buffer is read back from disk, so a simulator restart continues where it
// left off instead of losing or resending the queue.
func TestQueueSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sim.queue")
	now := time.Now().UTC()

	q, err := OpenQueue(path, time.Hour)
	if err != nil {
		t.Fatalf("OpenQueue: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := q.Append(entry(i, now)); err != nil {
			t.Fatal(err)
		}
	}
	// Replay two of them, then "crash".
	sent := 0
	q.Replay(now, func(Entry) error {
		if sent == 2 {
			return errors.New("stop")
		}
		sent++
		return nil
	})
	if err := q.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := OpenQueue(path, time.Hour)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	if got := reopened.Pending(); got != 3 {
		t.Fatalf("Pending after reopen = %d, want 3", got)
	}
	var rest []string
	if result := reopened.Replay(now, func(e Entry) error {
		rest = append(rest, string(e.Payload))
		return nil
	}); result.Err != nil {
		t.Fatalf("replay after reopen: %v", result.Err)
	}
	if len(rest) != 3 || rest[0] != `{"seq":2}` {
		t.Errorf("after reopen got %v, want the three entries starting at seq 2", rest)
	}
}

// A fully drained buffer is truncated so it does not grow without bound.
func TestQueueCompactsWhenDrained(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sim.queue")
	q, err := OpenQueue(path, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	now := time.Now().UTC()
	for i := 0; i < 50; i++ {
		if err := q.Append(entry(i, now)); err != nil {
			t.Fatal(err)
		}
	}
	if result := q.Replay(now, func(Entry) error { return nil }); result.Err != nil {
		t.Fatalf("Replay: %v", result.Err)
	}

	info, err := q.file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Errorf("queue file is %d bytes after a full replay, want 0", info.Size())
	}

	// And it still works afterwards.
	if err := q.Append(entry(99, now)); err != nil {
		t.Fatal(err)
	}
	var got string
	if result := q.Replay(now, func(e Entry) error {
		got = string(e.Payload)
		return nil
	}); result.Err != nil {
		t.Fatalf("replay after compaction: %v", result.Err)
	}
	if got != `{"seq":99}` {
		t.Errorf("after compaction got %q", got)
	}
}
