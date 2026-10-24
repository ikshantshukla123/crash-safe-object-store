// Package wal implements the write-ahead log for storage-node file
// operations.
package wal

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
)

// RecordType identifies the kind of intent in a WAL record.
type RecordType uint8

const (
	RecordBeginPut    RecordType = 1
	RecordCommitPut   RecordType = 2
	RecordAbortPut    RecordType = 3
	RecordBeginDelete RecordType = 4
	RecordDoneDelete  RecordType = 5
	RecordCheckpoint  RecordType = 6
)

func (t RecordType) String() string {
	switch t {
	case RecordBeginPut:
		return "BEGIN_PUT"
	case RecordCommitPut:
		return "COMMIT_PUT"
	case RecordAbortPut:
		return "ABORT_PUT"
	case RecordBeginDelete:
		return "BEGIN_DELETE"
	case RecordDoneDelete:
		return "DONE_DELETE"
	case RecordCheckpoint:
		return "CHECKPOINT"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", uint8(t))
	}
}

func (t RecordType) valid() bool {
	return t >= RecordBeginPut && t <= RecordCheckpoint
}

const (
	headerSize    = 8
	fixedBodySize = 8 + 1
	maxPayload    = 1 << 20
	maxRecordSize = fixedBodySize + maxPayload
)

var (
	ErrShortRecord   = errors.New("wal: short record")
	ErrBadCRC        = errors.New("wal: checksum mismatch")
	ErrInvalidRecord = errors.New("wal: invalid record")
	ErrClosed        = errors.New("wal: log is closed")
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

// Record is one decoded WAL record.
type Record struct {
	LSN     uint64
	Type    RecordType
	Payload []byte
}

// PutIntent is the payload of BEGIN_PUT. It contains everything replay needs
// to finish or abort an in-doubt write without the original request.
type PutIntent struct {
	ChunkID    string   `json:"chunk_id"`
	UploadID   string   `json:"upload_id"`
	PartNo     int32    `json:"part_no"`
	ChunkIndex int32    `json:"chunk_index"`
	SizeBytes  int64    `json:"size_bytes"`
	SHA256     string   `json:"sha256"`
	NodeIDs    []string `json:"node_ids"`
}

// ChunkRef is the payload of COMMIT_PUT and ABORT_PUT.
type ChunkRef struct {
	ChunkID string `json:"chunk_id"`
}

// DeleteIntent is the payload of BEGIN_DELETE.
type DeleteIntent struct {
	ChunkIDs []string `json:"chunk_ids"`
	NodeIDs  []string `json:"node_ids"`
}

// CheckpointPayload records that every operation up to ThroughLSN is resolved.
type CheckpointPayload struct {
	ThroughLSN uint64 `json:"through_lsn"`
}

// Encode serialises one record:
//
//	len u32 | crc32c u32 | lsn u64 | type u8 | payload
//
// len is the number of bytes after the crc: lsn, type and payload. The CRC
// covers exactly those bytes.
func Encode(lsn uint64, typ RecordType, payload []byte) ([]byte, error) {
	if !typ.valid() {
		return nil, fmt.Errorf("%w: type %d", ErrInvalidRecord, typ)
	}
	if len(payload) > maxPayload {
		return nil, fmt.Errorf("%w: payload is %d bytes, limit is %d", ErrInvalidRecord, len(payload), maxPayload)
	}

	bodyLen := fixedBodySize + len(payload)
	b := make([]byte, headerSize+bodyLen)
	binary.BigEndian.PutUint32(b[0:4], uint32(bodyLen))
	binary.BigEndian.PutUint64(b[8:16], lsn)
	b[16] = byte(typ)
	copy(b[17:], payload)
	binary.BigEndian.PutUint32(b[4:8], crc32.Checksum(b[8:], crcTable))
	return b, nil
}

// Decode reads one record from the front of b and returns the number of bytes
// consumed.
func Decode(b []byte) (Record, int, error) {
	if len(b) < headerSize {
		return Record{}, 0, fmt.Errorf("%w: header needs %d bytes, have %d", ErrShortRecord, headerSize, len(b))
	}
	bodyLen := binary.BigEndian.Uint32(b[0:4])
	if bodyLen < fixedBodySize || bodyLen > maxRecordSize {
		return Record{}, 0, fmt.Errorf("%w: body length %d", ErrInvalidRecord, bodyLen)
	}
	total := headerSize + int(bodyLen)
	if len(b) < total {
		return Record{}, 0, fmt.Errorf("%w: record needs %d bytes, have %d", ErrShortRecord, total, len(b))
	}
	if binary.BigEndian.Uint32(b[4:8]) != crc32.Checksum(b[8:total], crcTable) {
		return Record{}, 0, ErrBadCRC
	}
	typ := RecordType(b[16])
	if !typ.valid() {
		return Record{}, 0, fmt.Errorf("%w: type %d", ErrInvalidRecord, typ)
	}
	payload := make([]byte, int(bodyLen)-fixedBodySize)
	copy(payload, b[17:total])
	return Record{
		LSN:     binary.BigEndian.Uint64(b[8:16]),
		Type:    typ,
		Payload: payload,
	}, total, nil
}

func marshalPayload(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal wal payload: %w", err)
	}
	return b, nil
}

func unmarshalPayload(rec Record, v any) error {
	if err := json.Unmarshal(rec.Payload, v); err != nil {
		return fmt.Errorf("%w: %s payload: %w", ErrInvalidRecord, rec.Type, err)
	}
	return nil
}
