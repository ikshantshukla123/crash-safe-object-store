package wal

import (
	"context"
	"fmt"
)

// Resolver completes or aborts operations left in doubt by a crash.
type Resolver interface {
	ResolvePut(ctx context.Context, intent PutIntent) error
	ResolveDelete(ctx context.Context, intent DeleteIntent) error
}

// RecoveryResult reports what recovery did.
type RecoveryResult struct {
	Records     int
	PendingPuts int
	PendingDels int
	LastLSN     uint64
	Truncated   bool
}

// Recover replays the log, resolves every in-doubt operation, appends a
// checkpoint and deletes the segments that checkpoint covers.
func Recover(ctx context.Context, dir string, segmentBytes int64, resolver Resolver, opts ...Option) (*Log, RecoveryResult, error) {
	replayed, err := Replay(dir)
	if err != nil {
		return nil, RecoveryResult{}, err
	}

	result := RecoveryResult{
		Records:   len(replayed.Records),
		LastLSN:   replayed.LastLSN,
		Truncated: replayed.Truncated,
	}

	type deleteOp struct {
		intent DeleteIntent
	}
	puts := make(map[string]PutIntent)
	var putOrder []string
	dels := make(map[string]deleteOp)
	var delOrder []string

	for _, rec := range replayed.Records {
		switch rec.Type {
		case RecordBeginPut:
			var intent PutIntent
			if err := unmarshalPayload(rec, &intent); err != nil {
				return nil, result, err
			}
			if intent.ChunkID == "" {
				return nil, result, fmt.Errorf("%w: BEGIN_PUT without chunk id", ErrInvalidRecord)
			}
			if _, ok := puts[intent.ChunkID]; !ok {
				putOrder = append(putOrder, intent.ChunkID)
			}
			puts[intent.ChunkID] = intent
		case RecordCommitPut, RecordAbortPut:
			var ref ChunkRef
			if err := unmarshalPayload(rec, &ref); err != nil {
				return nil, result, err
			}
			delete(puts, ref.ChunkID)
		case RecordBeginDelete:
			var intent DeleteIntent
			if err := unmarshalPayload(rec, &intent); err != nil {
				return nil, result, err
			}
			key := deleteKey(intent)
			if _, ok := dels[key]; !ok {
				delOrder = append(delOrder, key)
			}
			dels[key] = deleteOp{intent: intent}
		case RecordDoneDelete:
			var intent DeleteIntent
			if err := unmarshalPayload(rec, &intent); err != nil {
				return nil, result, err
			}
			delete(dels, deleteKey(intent))
		case RecordCheckpoint:
			var cp CheckpointPayload
			if err := unmarshalPayload(rec, &cp); err != nil {
				return nil, result, err
			}
		default:
			return nil, result, fmt.Errorf("%w: unexpected type %s", ErrInvalidRecord, rec.Type)
		}
	}

	if resolver == nil && (len(puts) > 0 || len(dels) > 0) {
		return nil, result, fmt.Errorf("%w: %d in-doubt operations need a resolver", ErrInvalidRecord, len(puts)+len(dels))
	}
	if resolver != nil {
		for _, id := range putOrder {
			intent, ok := puts[id]
			if !ok {
				continue
			}
			if err := resolver.ResolvePut(ctx, intent); err != nil {
				return nil, result, fmt.Errorf("resolve put %s: %w", id, err)
			}
			result.PendingPuts++
		}
		for _, key := range delOrder {
			op, ok := dels[key]
			if !ok {
				continue
			}
			if err := resolver.ResolveDelete(ctx, op.intent); err != nil {
				return nil, result, fmt.Errorf("resolve delete %s: %w", key, err)
			}
			result.PendingDels++
		}
	}

	log, err := Open(dir, segmentBytes, replayed.LastLSN+1, opts...)
	if err != nil {
		return nil, result, err
	}

	checkpoint := CheckpointPayload{ThroughLSN: replayed.LastLSN}
	if _, err := log.AppendSync(ctx, RecordCheckpoint, checkpoint); err != nil {
		_ = log.Close(context.Background())
		return nil, result, fmt.Errorf("append recovery checkpoint: %w", err)
	}
	if err := log.RemoveInactiveSegments(); err != nil {
		_ = log.Close(context.Background())
		return nil, result, err
	}
	return log, result, nil
}

func deleteKey(intent DeleteIntent) string {
	if len(intent.ChunkIDs) == 0 {
		return ""
	}
	return intent.ChunkIDs[0]
}

// CheckOpen reports whether dir contains a readable log. It is used by tests
// and by the API's startup self-check.
func CheckOpen(dir string) error {
	_, err := Replay(dir)
	return err
}
