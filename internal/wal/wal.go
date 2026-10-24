package wal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

const (
	defaultRequestBuffer = 256
	maxBatchSize         = 256
)

type request struct {
	typ     RecordType
	payload []byte
	sync    bool
	close   bool
	resp    chan response
	result  response
}

type response struct {
	lsn uint64
	err error
}

// Option configures a Log.
type Option func(*Log)

// WithFatalHandler replaces the default fsync-failure handler, which logs and
// exits the process. Tests use this to observe the fatal path.
func WithFatalHandler(fn func(error)) Option {
	return func(l *Log) {
		if fn != nil {
			l.fatal = fn
		}
	}
}

// Log is the single-writer WAL. All appends go through one goroutine, which
// batches records arriving during an fsync into a group commit.
type Log struct {
	dir          string
	segmentBytes int64
	fatal        func(error)
	reqCh        chan request
	done         chan struct{}
	closeOnce    sync.Once
	closed       atomic.Bool

	mu       sync.Mutex
	file     *os.File
	fileName string
	seq      uint64
	fileSize int64
	nextLSN  uint64
}

// Open creates a new segment and starts the writer goroutine. The caller must
// have replayed the directory already and pass the first unused LSN.
func Open(dir string, segmentBytes int64, nextLSN uint64, opts ...Option) (*Log, error) {
	if dir == "" {
		return nil, errors.New("wal dir must not be empty")
	}
	if segmentBytes <= 0 {
		return nil, errors.New("wal segment size must be positive")
	}
	if nextLSN == 0 {
		nextLSN = 1
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create wal dir %s: %w", dir, err)
	}

	l := &Log{
		dir:          dir,
		segmentBytes: segmentBytes,
		fatal:        defaultFatal,
		reqCh:        make(chan request, defaultRequestBuffer),
		done:         make(chan struct{}),
		nextLSN:      nextLSN,
	}
	for _, opt := range opts {
		opt(l)
	}

	segments, err := ListSegments(dir)
	if err != nil {
		return nil, err
	}
	if len(segments) > 0 {
		seq, err := parseSegmentSeq(filepath.Base(segments[len(segments)-1]))
		if err != nil {
			return nil, err
		}
		l.seq = seq + 1
	}
	if err := l.createSegment(); err != nil {
		return nil, err
	}
	go l.run()
	return l, nil
}

func defaultFatal(err error) {
	slog.Error("wal fsync failed; exiting for replay", "err", err)
	os.Exit(1)
}

func (l *Log) createSegment() error {
	name := segmentName(l.seq)
	path := filepath.Join(l.dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create wal segment %s: %w", path, err)
	}
	if err := fsyncDir(l.dir); err != nil {
		f.Close()
		return err
	}
	l.mu.Lock()
	l.file = f
	l.fileName = name
	l.fileSize = 0
	l.mu.Unlock()
	return nil
}

func (l *Log) rotate() error {
	if l.file != nil {
		if err := l.file.Sync(); err != nil {
			return fmt.Errorf("fsync wal segment before rotation: %w", err)
		}
		if err := l.file.Close(); err != nil {
			return fmt.Errorf("close wal segment before rotation: %w", err)
		}
	}
	l.seq++
	return l.createSegment()
}

// Append writes a record without fsyncing it.
func (l *Log) Append(ctx context.Context, typ RecordType, payload any) (uint64, error) {
	return l.append(ctx, typ, payload, false)
}

// AppendSync writes a record and waits for it to be fsynced.
func (l *Log) AppendSync(ctx context.Context, typ RecordType, payload any) (uint64, error) {
	return l.append(ctx, typ, payload, true)
}

func (l *Log) append(ctx context.Context, typ RecordType, payload any, sync bool) (uint64, error) {
	if l.closed.Load() {
		return 0, ErrClosed
	}
	raw, err := marshalPayload(payload)
	if err != nil {
		return 0, err
	}
	resp := make(chan response, 1)
	req := request{typ: typ, payload: raw, sync: sync, resp: resp}
	select {
	case l.reqCh <- req:
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-l.done:
		return 0, ErrClosed
	}
	select {
	case r := <-resp:
		return r.lsn, r.err
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-l.done:
		return 0, ErrClosed
	}
}

func (l *Log) run() {
	defer close(l.done)
	for req := range l.reqCh {
		if req.close {
			l.finishClose(req)
			return
		}
		batch := []request{req}
		for len(batch) < maxBatchSize {
			select {
			case next := <-l.reqCh:
				if next.close {
					l.writeBatch(batch)
					l.finishClose(next)
					return
				}
				batch = append(batch, next)
			default:
				l.writeBatch(batch)
				goto drained
			}
		}
		l.writeBatch(batch)
	drained:
	}
}

func (l *Log) writeBatch(batch []request) {
	syncNeeded := false
	for i := range batch {
		raw, err := Encode(l.nextLSN, batch[i].typ, batch[i].payload)
		if err != nil {
			batch[i].result = response{err: err}
			continue
		}
		if l.file == nil || (l.fileSize > 0 && l.fileSize+int64(len(raw)) > l.segmentBytes) {
			if err := l.rotate(); err != nil {
				l.failAll(fmt.Errorf("rotate wal: %w", err), batch)
				return
			}
		}
		n, err := l.file.WriteAt(raw, l.fileSize)
		if err != nil || n != len(raw) {
			if err == nil {
				err = fmt.Errorf("short wal write: wrote %d of %d bytes", n, len(raw))
			}
			l.failAll(fmt.Errorf("write wal record: %w", err), batch)
			return
		}
		l.fileSize += int64(n)
		batch[i].result = response{lsn: l.nextLSN}
		l.nextLSN++
		if batch[i].sync {
			syncNeeded = true
		}
	}

	if syncNeeded {
		if err := l.file.Sync(); err != nil {
			err = fmt.Errorf("fsync wal: %w", err)
			for i := range batch {
				batch[i].result = response{err: err}
			}
			l.sendAll(batch)
			l.fatal(err)
			return
		}
	}
	l.sendAll(batch)
}

func (l *Log) sendAll(batch []request) {
	for i := range batch {
		batch[i].resp <- batch[i].result
	}
}

func (l *Log) failAll(err error, batch []request) {
	for i := range batch {
		batch[i].result = response{err: err}
	}
	l.sendAll(batch)
	l.fatal(err)
}

func (l *Log) finishClose(req request) {
	var err error
	if l.file != nil {
		if syncErr := l.file.Sync(); syncErr != nil {
			err = fmt.Errorf("fsync wal on close: %w", syncErr)
		}
		if closeErr := l.file.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close wal: %w", closeErr)
		}
	}
	req.resp <- response{err: err}
}

// Close drains queued records, fsyncs and closes the active segment.
func (l *Log) Close(ctx context.Context) error {
	var err error
	l.closeOnce.Do(func() {
		l.closed.Store(true)
		resp := make(chan response, 1)
		select {
		case l.reqCh <- request{close: true, resp: resp}:
		case <-ctx.Done():
			err = ctx.Err()
			return
		case <-l.done:
			return
		}
		select {
		case r := <-resp:
			err = r.err
		case <-ctx.Done():
			err = ctx.Err()
		case <-l.done:
		}
	})
	if err == nil {
		select {
		case <-l.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

// RemoveInactiveSegments deletes every segment except the active one. Call it
// only after a checkpoint covering the deleted records has been fsynced.
func (l *Log) RemoveInactiveSegments() error {
	l.mu.Lock()
	active := l.fileName
	l.mu.Unlock()

	segments, err := ListSegments(l.dir)
	if err != nil {
		return err
	}
	for _, path := range segments {
		if filepath.Base(path) == active {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove checkpointed wal segment %s: %w", path, err)
		}
	}
	return fsyncDir(l.dir)
}

func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open dir %s for fsync: %w", dir, err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("fsync dir %s: %w", dir, err)
	}
	return nil
}
