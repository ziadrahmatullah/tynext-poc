package sim

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Queue is the store-and-forward buffer. When the link to the broker is down
// the simulator appends here instead of dropping messages, and replays them in
// order once it reconnects — with the original ts, so the gap is filled with
// the time the plant actually measured, not the time we caught up.
//
// The file is append-only JSONL with a separate cursor file. Ordering is
// therefore free, and a crash costs at most the entries after the last cursor
// write, which are replayed again and deduplicated by the database.
type Queue struct {
	path   string
	cursor string
	maxAge time.Duration

	mu      sync.Mutex
	file    *os.File
	offset  int64
	pending int
}

// Entry is one buffered publish.
type Entry struct {
	Topic   string    `json:"topic"`
	Payload []byte    `json:"payload"`
	QoS     byte      `json:"qos"`
	Retain  bool      `json:"retain"`
	TS      time.Time `json:"ts"`
}

// OpenQueue opens or creates the buffer at path. maxAge bounds how far back
// replay reaches; older entries are dropped on the way out.
//
// TBC: 72 h is a placeholder until the OT team tells us how much TF6701 itself
// can buffer — matching that figure is the point.
func OpenQueue(path string, maxAge time.Duration) (*Queue, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create queue directory: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open queue %s: %w", path, err)
	}

	q := &Queue{path: path, cursor: path + ".offset", maxAge: maxAge, file: f}
	if err := q.loadCursor(); err != nil {
		f.Close()
		return nil, err
	}
	if err := q.countPending(); err != nil {
		f.Close()
		return nil, err
	}
	return q, nil
}

// Close flushes the cursor and closes the file.
func (q *Queue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.file == nil {
		return nil
	}
	err := errors.Join(q.saveCursorLocked(), q.file.Sync(), q.file.Close())
	q.file = nil
	return err
}

// Append buffers one publish.
func (q *Queue) Append(e Entry) error {
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode queue entry: %w", err)
	}
	line = append(line, '\n')

	q.mu.Lock()
	defer q.mu.Unlock()
	if q.file == nil {
		return errors.New("queue is closed")
	}
	if _, err := q.file.Write(line); err != nil {
		return fmt.Errorf("append to queue: %w", err)
	}
	q.pending++
	return nil
}

// Pending reports how many entries are waiting to be replayed.
func (q *Queue) Pending() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.pending
}

// Path is where the buffer lives on disk.
func (q *Queue) Path() string { return q.path }

// ReplayResult summarises one replay pass.
type ReplayResult struct {
	Sent    int
	Expired int
	// Err is the publish error that stopped the pass, if any. Progress up to
	// that point is kept, so the next pass continues instead of resending.
	Err error
}

// Replay sends everything buffered, in the order it was appended, calling
// publish for each entry. It stops at the first publish error and keeps the
// cursor there.
func (q *Queue) Replay(now time.Time, publish func(Entry) error) ReplayResult {
	q.mu.Lock()
	defer q.mu.Unlock()

	var result ReplayResult
	if q.file == nil || q.pending == 0 {
		return result
	}

	reader, err := os.Open(q.path)
	if err != nil {
		result.Err = fmt.Errorf("open queue for replay: %w", err)
		return result
	}
	defer reader.Close()

	if _, err := reader.Seek(q.offset, 0); err != nil {
		result.Err = fmt.Errorf("seek queue: %w", err)
		return result
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		advance := int64(len(line) + 1)

		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			// A torn line from a crash: skip it rather than stall forever.
			q.offset += advance
			q.pending--
			continue
		}

		if q.maxAge > 0 && now.Sub(e.TS) > q.maxAge {
			q.offset += advance
			q.pending--
			result.Expired++
			continue
		}

		if err := publish(e); err != nil {
			result.Err = err
			break
		}
		q.offset += advance
		q.pending--
		result.Sent++
	}

	if err := scanner.Err(); err != nil && result.Err == nil {
		result.Err = fmt.Errorf("read queue: %w", err)
	}

	if saveErr := q.saveCursorLocked(); saveErr != nil && result.Err == nil {
		result.Err = saveErr
	}
	if q.pending <= 0 && result.Err == nil {
		if err := q.compactLocked(); err != nil {
			result.Err = err
		}
	}
	return result
}

// compactLocked truncates a fully replayed buffer so it does not grow forever.
func (q *Queue) compactLocked() error {
	if err := q.file.Truncate(0); err != nil {
		return fmt.Errorf("truncate queue: %w", err)
	}
	if _, err := q.file.Seek(0, 0); err != nil {
		return fmt.Errorf("rewind queue: %w", err)
	}
	q.offset = 0
	q.pending = 0
	return q.saveCursorLocked()
}

func (q *Queue) loadCursor() error {
	raw, err := os.ReadFile(q.cursor)
	if errors.Is(err, os.ErrNotExist) {
		q.offset = 0
		return nil
	}
	if err != nil {
		return fmt.Errorf("read queue cursor: %w", err)
	}
	// An unreadable cursor means we replay from the start. That costs a few
	// duplicates, which the database discards, instead of losing data.
	offset, parseErr := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if parseErr != nil {
		offset = 0
	}

	info, err := q.file.Stat()
	if err != nil {
		return fmt.Errorf("stat queue: %w", err)
	}
	// A cursor past the end means the file was replaced; start over.
	if offset > info.Size() || offset < 0 {
		offset = 0
	}
	q.offset = offset
	return nil
}

func (q *Queue) saveCursorLocked() error {
	if err := os.WriteFile(q.cursor, []byte(fmt.Sprintf("%d\n", q.offset)), 0o600); err != nil {
		return fmt.Errorf("write queue cursor: %w", err)
	}
	return nil
}

// countPending counts the lines after the cursor so Pending is right even after
// a restart.
func (q *Queue) countPending() error {
	reader, err := os.Open(q.path)
	if err != nil {
		return fmt.Errorf("open queue: %w", err)
	}
	defer reader.Close()

	if _, err := reader.Seek(q.offset, 0); err != nil {
		return fmt.Errorf("seek queue: %w", err)
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	count := 0
	for scanner.Scan() {
		count++
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read queue: %w", err)
	}
	q.pending = count
	return nil
}
