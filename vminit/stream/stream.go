// Package stream provides stream management for vminit I/O.
package stream

import "io"

type Manager interface {
	Get(id uint32) (io.ReadWriteCloser, error)
}
