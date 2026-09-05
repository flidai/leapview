package arrowresult

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/ipc"
)

const (
	// DefaultMaxIPCBytes bounds a serialized Arrow result at the codec
	// boundary. Callers with a tighter tier limit should use EncodeIPCWithLimit
	// and DecodeIPCWithLimit.
	DefaultMaxIPCBytes int64 = 128 << 20
	MaxIPCBytes        int64 = 1 << 30
)

var (
	ErrIPCTooLarge = errors.New("Arrow IPC payload exceeds byte limit")
	ErrInvalidIPC  = errors.New("invalid Arrow IPC payload")
)

type boundedBuffer struct {
	bytes.Buffer
	max int64
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if int64(b.Len())+int64(len(p)) > b.max {
		return 0, ErrIPCTooLarge
	}
	return b.Buffer.Write(p)
}

// EncodeIPC serializes all record batches pinned by lease into one canonical
// Arrow IPC stream. The lease is borrowed and remains owned by the caller.
func EncodeIPC(lease *Lease) ([]byte, error) {
	return EncodeIPCWithLimit(lease, DefaultMaxIPCBytes)
}

// EncodeIPCWithLimit is EncodeIPC with an explicit serialized-byte limit.
func EncodeIPCWithLimit(lease *Lease, maxBytes int64) ([]byte, error) {
	if lease == nil || lease.Schema() == nil {
		return nil, fmt.Errorf("%w: result schema is required", ErrInvalidIPC)
	}
	if maxBytes <= 0 || maxBytes > MaxIPCBytes {
		return nil, fmt.Errorf("%w: invalid maximum IPC size", ErrInvalidIPC)
	}
	var buffer boundedBuffer
	buffer.max = maxBytes
	writer := ipc.NewWriter(&buffer, ipc.WithSchema(lease.Schema()))
	err := lease.VisitRecords(func(record arrow.RecordBatch) error {
		return writer.Write(record)
	})
	if err == nil {
		err = writer.Close()
	} else {
		_ = writer.Close()
	}
	if err != nil {
		if errors.Is(err, ErrIPCTooLarge) {
			return nil, ErrIPCTooLarge
		}
		return nil, fmt.Errorf("%w: encode stream: %v", ErrInvalidIPC, err)
	}
	return append([]byte(nil), buffer.Bytes()...), nil
}

// DecodeIPC decodes a bounded Arrow IPC stream into an owned immutable
// Result. The returned result owns its buffers and must be released exactly
// once by the caller.
func DecodeIPC(payload []byte) (*Result, error) {
	return DecodeIPCWithLimit(payload, DefaultMaxIPCBytes)
}

// DecodeIPCWithLimit is DecodeIPC with an explicit serialized-byte limit.
func DecodeIPCWithLimit(payload []byte, maxBytes int64) (*Result, error) {
	if maxBytes <= 0 || maxBytes > MaxIPCBytes {
		return nil, fmt.Errorf("%w: invalid maximum IPC size", ErrInvalidIPC)
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("%w: empty stream", ErrInvalidIPC)
	}
	if int64(len(payload)) > maxBytes {
		return nil, ErrIPCTooLarge
	}
	// Arrow stream IPC terminates with the eight-byte continuation EOS marker.
	// Requiring it at the end rejects bytes appended after a valid stream that
	// ipc.Reader intentionally does not inspect once it reaches EOS.
	if len(payload) < 8 || !bytes.Equal(payload[len(payload)-8:], []byte{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0}) {
		return nil, fmt.Errorf("%w: missing stream terminator", ErrInvalidIPC)
	}
	reader, err := ipc.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%w: open stream: %v", ErrInvalidIPC, err)
	}
	defer reader.Release()
	schema := reader.Schema()
	if schema == nil {
		if err := reader.Err(); err != nil {
			return nil, fmt.Errorf("%w: read schema: %v", ErrInvalidIPC, err)
		}
		return nil, fmt.Errorf("%w: missing schema", ErrInvalidIPC)
	}
	builder := NewBuilder()
	if err := builder.WriteSchema(schema); err != nil {
		builder.Abort()
		return nil, err
	}
	for reader.Next() {
		if err := builder.WriteRecord(reader.RecordBatch()); err != nil {
			builder.Abort()
			return nil, fmt.Errorf("%w: copy record: %v", ErrInvalidIPC, err)
		}
	}
	if err := reader.Err(); err != nil {
		builder.Abort()
		return nil, fmt.Errorf("%w: read record: %v", ErrInvalidIPC, err)
	}
	result, err := builder.Finish()
	if err != nil {
		return nil, fmt.Errorf("%w: finish result: %v", ErrInvalidIPC, err)
	}
	if result.Bytes() > maxBytes {
		result.Release()
		return nil, ErrIPCTooLarge
	}
	return result, nil
}
