// Package iobuf provides a shared buffer pool for I/O operations.
package iobuf

import "sync"

// bufferSize is 4096 to align with PIPE_BUF on Linux for atomic pipe writes.
// See: http://man7.org/linux/man-pages/man7/pipe.7.html
const bufferSize = 4096

var pool = sync.Pool{
	New: func() any {
		buf := make([]byte, bufferSize)
		return &buf
	},
}

// Get returns a pooled 4096-byte buffer.
func Get() *[]byte {
	return pool.Get().(*[]byte)
}

// Put returns a buffer to the pool.
func Put(buf *[]byte) {
	if buf == nil {
		return
	}
	pool.Put(buf)
}
