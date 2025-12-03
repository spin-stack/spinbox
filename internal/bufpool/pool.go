// Package bufpool provides a shared buffer pool for I/O operations.
package bufpool

import "sync"

// Pool is a shared sync.Pool for 4096-byte buffers.
// This size aligns with PIPE_BUF on Linux for atomic pipe writes.
// See: http://man7.org/linux/man-pages/man7/pipe.7.html
var Pool = sync.Pool{
	New: func() interface{} {
		// Setting to 4096 to align with PIPE_BUF
		buffer := make([]byte, 4096)
		return &buffer
	},
}
