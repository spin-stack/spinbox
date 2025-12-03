package stream

import "io"

type Manager interface {
	Get(id uint32) (io.ReadWriteCloser, error)
}
