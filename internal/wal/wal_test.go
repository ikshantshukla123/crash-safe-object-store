package wal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func mustOpen(t *testing.T, dir string, segmentBytes int64, nextLSN uint64) *Log {
	t.Helper()
	l, err := Open(dir, segmentBytes, nextLSN)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		_ = l.Close(context.Background())
	})
	return l
}

func appendPut(t *testing.T, l *Log, id string, sync bool) {
	t.Helper()
	intent := PutIntent{ChunkID: id, UploadID: "upload", PartNo: 1, ChunkIndex: 0, SizeBytes: 3, SHA256: "abc", NodeIDs: []string{"n1", "n2"}}
	var err error
	if sync {
		_, err = l.AppendSync(context.Background(), RecordBeginPut, intent)
	} else {
		_, err = l.Append(context.Background(), RecordBeginPut, intent)
	}
	if err != nil {
		t.Fatalf("append put %s: %v", id, err)
	}
}

func writeRawSegment(t *testing.T, dir string, data []byte) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, segmentName(1))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write segment: %v", err)
	}
	return path
}

func encodeRecord(t *testing.T, lsn uint64, typ RecordType, payload any) []byte {
	t.Helper()
	raw, err := marshalPayload(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	b, err := Encode(lsn, typ, raw)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return b
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	payload := PutIntent{ChunkID: "c1", SHA256: "deadbeef", NodeIDs: []string{"n1"}}
	raw := encodeRecord(t, 42, RecordBeginPut, payload)

	rec, n, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if n != len(raw) {
		t.Errorf("consumed %d bytes, want %d", n, len(raw))
	}
	if rec.LSN != 42 || rec.Type != RecordBeginPut {
		t.Errorf("record = lsn %d type %s", rec.LSN, rec.Type)
	}
	var got PutIntent
	if err := unmarshalPayload(rec, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ChunkID != "c1" || len(got.NodeIDs) != 1 {
		t.Errorf("payload = %+v", got)
	}
}

func TestDecodeRejectsBadCRC(t *testing.T) {
	raw := encodeRecord(t, 1, RecordCommitPut, ChunkRef{ChunkID: "c1"})
	raw[len(raw)-1] ^= 0xff
	if _, _, err := Decode(raw); !errors.Is(err, ErrBadCRC) {
		t.Errorf("Decode error = %v, want ErrBadCRC", err)
	}
}

// Truncating a valid log at any byte offset must yield exactly the prefix of
// complete records, with no panic and no invented record.
func TestReplayTruncatedAtEveryByteOffset(t *testing.T) {
	var data []byte
	for i := 1; i <= 5; i++ {
		data = append(data, encodeRecord(t, uint64(i), RecordBeginPut, PutIntent{
			ChunkID:  string(rune('a' + i)),
			UploadID: "u",
			PartNo:   int32(i),
			NodeIDs:  []string{"n1", "n2", "n3"},
		})...)
	}

	for cut := 0; cut <= len(data); cut++ {
		dir := t.TempDir()
		writeRawSegment(t, dir, data[:cut])

		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("cut %d panicked: %v", cut, r)
				}
			}()
			res, err := Replay(dir)
			if err != nil {
				t.Fatalf("cut %d: Replay: %v", cut, err)
			}
			want := completeRecords(t, data[:cut])
			if len(res.Records) != len(want) {
				t.Fatalf("cut %d: got %d records, want %d", cut, len(res.Records), len(want))
			}
			for i := range want {
				if res.Records[i].LSN != want[i] {
					t.Fatalf("cut %d: record %d has lsn %d, want %d", cut, i, res.Records[i].LSN, want[i])
				}
			}
			wantTruncated := recordPrefixLen(data, cut) != cut
			if res.Truncated != wantTruncated {
				t.Fatalf("cut %d: Truncated = %v, want %v", cut, res.Truncated, wantTruncated)
			}
			info, err := os.Stat(filepath.Join(dir, segmentName(1)))
			if err != nil {
				t.Fatalf("stat: %v", err)
			}
			if info.Size() != int64(recordPrefixLen(data, cut)) {
				t.Fatalf("cut %d: file size %d, want %d", cut, info.Size(), recordPrefixLen(data, cut))
			}
		}()
	}
}

func completeRecords(t *testing.T, data []byte) []uint64 {
	t.Helper()
	var out []uint64
	off := 0
	for off < len(data) {
		rec, n, err := Decode(data[off:])
		if err != nil {
			break
		}
		out = append(out, rec.LSN)
		off += n
	}
	return out
}

func recordPrefixLen(data []byte, cut int) int {
	off := 0
	for off < cut {
		_, n, err := Decode(data[off:cut])
		if err != nil {
			break
		}
		off += n
	}
	return off
}

func TestReplayBadRecordFollowedByValidFails(t *testing.T) {
	good1 := encodeRecord(t, 1, RecordBeginPut, PutIntent{ChunkID: "a", NodeIDs: []string{"n1"}})
	bad := encodeRecord(t, 2, RecordBeginPut, PutIntent{ChunkID: "b", NodeIDs: []string{"n1"}})
	bad[len(bad)-1] ^= 0xff
	good3 := encodeRecord(t, 3, RecordBeginPut, PutIntent{ChunkID: "c", NodeIDs: []string{"n1"}})
	dir := t.TempDir()
	writeRawSegment(t, dir, append(append(append([]byte{}, good1...), bad...), good3...))

	if _, err := Replay(dir); err == nil {
		t.Fatal("Replay accepted corruption in the middle of a segment")
	}
}

func TestReplayTwiceIsIdentical(t *testing.T) {
	dir := t.TempDir()
	l := mustOpen(t, dir, 1<<20, 1)
	appendPut(t, l, "a", true)
	appendPut(t, l, "b", true)
	if err := l.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	first, err := Replay(dir)
	if err != nil {
		t.Fatalf("first Replay: %v", err)
	}
	second, err := Replay(dir)
	if err != nil {
		t.Fatalf("second Replay: %v", err)
	}
	if len(first.Records) != len(second.Records) || first.LastLSN != second.LastLSN {
		t.Fatalf("replay changed: first %d/%d second %d/%d",
			len(first.Records), first.LastLSN, len(second.Records), second.LastLSN)
	}
}

func TestGroupCommitWritesEveryRecord(t *testing.T) {
	dir := t.TempDir()
	l := mustOpen(t, dir, 1<<20, 1)

	const n = 64
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := l.AppendSync(context.Background(), RecordBeginPut, PutIntent{
				ChunkID:  string(rune('a' + i%26)),
				PartNo:   int32(i + 1),
				NodeIDs:  []string{"n1"},
				UploadID: "u",
			})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := l.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	res, err := Replay(dir)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(res.Records) != n {
		t.Fatalf("replayed %d records, want %d", len(res.Records), n)
	}
	for i := 1; i < len(res.Records); i++ {
		if res.Records[i].LSN <= res.Records[i-1].LSN {
			t.Fatalf("LSNs are not increasing at %d: %d then %d", i, res.Records[i-1].LSN, res.Records[i].LSN)
		}
	}
}

func TestSegmentRotation(t *testing.T) {
	dir := t.TempDir()
	l := mustOpen(t, dir, 128, 1)
	for i := 0; i < 10; i++ {
		appendPut(t, l, "chunk-"+string(rune('a'+i)), true)
	}
	if err := l.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	segments, err := ListSegments(dir)
	if err != nil {
		t.Fatalf("ListSegments: %v", err)
	}
	if len(segments) < 2 {
		t.Fatalf("expected rotation, got %d segment(s)", len(segments))
	}
	res, err := Replay(dir)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(res.Records) != 10 {
		t.Fatalf("replayed %d records, want 10", len(res.Records))
	}
}

type fakeResolver struct {
	puts    []PutIntent
	dels    []DeleteIntent
	failPut error
}

func (f *fakeResolver) ResolvePut(_ context.Context, intent PutIntent) error {
	if f.failPut != nil {
		return f.failPut
	}
	f.puts = append(f.puts, intent)
	return nil
}

func (f *fakeResolver) ResolveDelete(_ context.Context, intent DeleteIntent) error {
	f.dels = append(f.dels, intent)
	return nil
}

func TestRecoverResolvesOnlyInDoubtOperations(t *testing.T) {
	dir := t.TempDir()
	l := mustOpen(t, dir, 1<<20, 1)
	appendPut(t, l, "committed", true)
	if _, err := l.AppendSync(context.Background(), RecordCommitPut, ChunkRef{ChunkID: "committed"}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	appendPut(t, l, "pending", true)
	if _, err := l.AppendSync(context.Background(), RecordBeginDelete, DeleteIntent{
		ChunkIDs: []string{"gone"},
		NodeIDs:  []string{"n1"},
	}); err != nil {
		t.Fatalf("begin delete: %v", err)
	}
	if err := l.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	resolver := &fakeResolver{}
	recovered, result, err := Recover(context.Background(), dir, 1<<20, resolver)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	defer recovered.Close(context.Background())

	if result.PendingPuts != 1 || len(resolver.puts) != 1 || resolver.puts[0].ChunkID != "pending" {
		t.Fatalf("puts = %+v, result = %+v", resolver.puts, result)
	}
	if result.PendingDels != 1 || len(resolver.dels) != 1 {
		t.Fatalf("deletes = %+v, result = %+v", resolver.dels, result)
	}

	segments, err := ListSegments(dir)
	if err != nil {
		t.Fatalf("ListSegments: %v", err)
	}
	if len(segments) != 1 {
		t.Fatalf("checkpoint left %d segments, want 1", len(segments))
	}
	if err := recovered.Close(context.Background()); err != nil {
		t.Fatalf("closing recovered log: %v", err)
	}

	second := &fakeResolver{}
	recovered2, result2, err := Recover(context.Background(), dir, 1<<20, second)
	if err != nil {
		t.Fatalf("second Recover: %v", err)
	}
	defer recovered2.Close(context.Background())
	if len(second.puts) != 0 || len(second.dels) != 0 || result2.PendingPuts != 0 || result2.PendingDels != 0 {
		t.Fatalf("recovery is not idempotent: puts=%+v dels=%+v result=%+v", second.puts, second.dels, result2)
	}
}

func TestRecoverStopsOnResolverError(t *testing.T) {
	dir := t.TempDir()
	l := mustOpen(t, dir, 1<<20, 1)
	appendPut(t, l, "pending", true)
	if err := l.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	want := errors.New("boom")
	resolver := &fakeResolver{failPut: want}
	if _, _, err := Recover(context.Background(), dir, 1<<20, resolver); !errors.Is(err, want) {
		t.Fatalf("Recover error = %v, want %v", err, want)
	}
}

func TestAppendAfterCloseFails(t *testing.T) {
	dir := t.TempDir()
	l := mustOpen(t, dir, 1<<20, 1)
	if err := l.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := l.Append(context.Background(), RecordCheckpoint, CheckpointPayload{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Append after close = %v, want ErrClosed", err)
	}
}
