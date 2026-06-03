// Package workload provides payload generation helpers for the benchmark.
package workload

import (
	"bytes"
	"crypto/rand"
	"io"
)

// Payload holds a reusable random buffer for a given object size.
type Payload struct {
	data []byte
}

// NewPayload allocates a random buffer of the given size.
func NewPayload(size int64) (*Payload, error) {
	buf := make([]byte, size)
	if size > 0 {
		if _, err := rand.Read(buf); err != nil {
			return nil, err
		}
	}
	return &Payload{data: buf}, nil
}

// Size returns the payload length in bytes.
func (p *Payload) Size() int64 {
	return int64(len(p.data))
}

// Reader returns a fresh reader over the payload. Safe for concurrent use
// because each call yields an independent reader over the shared buffer.
func (p *Payload) Reader() io.Reader {
	return bytes.NewReader(p.data)
}
