package wal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const segmentExt = ".wal"

// ReplayResult is the outcome of reading every segment in order.
type ReplayResult struct {
	Records   []Record
	LastLSN   uint64
	Segments  []string
	Truncated bool
}

// ListSegments returns the segment paths in replay order.
func ListSegments(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read wal dir %s: %w", dir, err)
	}
	var segments []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), segmentExt) {
			continue
		}
		if _, err := parseSegmentSeq(e.Name()); err != nil {
			continue
		}
		segments = append(segments, filepath.Join(dir, e.Name()))
	}
	sort.Slice(segments, func(i, j int) bool {
		a, _ := parseSegmentSeq(filepath.Base(segments[i]))
		b, _ := parseSegmentSeq(filepath.Base(segments[j]))
		return a < b
	})
	return segments, nil
}

func parseSegmentSeq(name string) (uint64, error) {
	base := strings.TrimSuffix(name, segmentExt)
	if base == name {
		return 0, fmt.Errorf("not a segment name: %s", name)
	}
	seq, err := strconv.ParseUint(base, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse segment name %s: %w", name, err)
	}
	return seq, nil
}

func segmentName(seq uint64) string {
	return fmt.Sprintf("%016d%s", seq, segmentExt)
}

// Replay reads segments in order, truncating a torn tail at the first short
// read or bad checksum. A bad record followed by any valid data is treated as
// corruption and refuses to start.
func Replay(dir string) (ReplayResult, error) {
	segments, err := ListSegments(dir)
	if err != nil {
		return ReplayResult{}, err
	}

	result := ReplayResult{Segments: segments}
	for i, path := range segments {
		data, err := os.ReadFile(path)
		if err != nil {
			return ReplayResult{}, fmt.Errorf("read wal segment %s: %w", path, err)
		}
		offset := 0
		for offset < len(data) {
			rec, n, err := Decode(data[offset:])
			if err != nil {
				if i != len(segments)-1 || hasValidRecordAfter(data, offset+1, result.LastLSN) {
					return ReplayResult{}, fmt.Errorf("%w at %s:%d: %w", ErrInvalidRecord, path, offset, err)
				}
				if err := truncateSegment(path, int64(offset)); err != nil {
					return ReplayResult{}, err
				}
				result.Truncated = true
				return result, nil
			}
			if rec.LSN <= result.LastLSN {
				return ReplayResult{}, fmt.Errorf("%w at %s:%d: lsn %d is not increasing (last %d)",
					ErrInvalidRecord, path, offset, rec.LSN, result.LastLSN)
			}
			result.Records = append(result.Records, rec)
			result.LastLSN = rec.LSN
			offset += n
		}
		if offset == 0 && i != len(segments)-1 {
			return ReplayResult{}, fmt.Errorf("%w: empty segment %s is followed by %s",
				ErrInvalidRecord, path, segments[i+1])
		}
	}
	return result, nil
}

func truncateSegment(path string, size int64) error {
	if err := os.Truncate(path, size); err != nil {
		return fmt.Errorf("truncate wal segment %s: %w", path, err)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open wal segment %s for sync: %w", path, err)
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return fmt.Errorf("fsync truncated wal segment %s: %w", path, err)
	}
	return fsyncDir(filepath.Dir(path))
}

func hasValidRecordAfter(data []byte, start int, lastLSN uint64) bool {
	for off := start; off+headerSize <= len(data); off++ {
		rec, _, err := Decode(data[off:])
		if err == nil && rec.LSN > lastLSN {
			return true
		}
	}
	return false
}
