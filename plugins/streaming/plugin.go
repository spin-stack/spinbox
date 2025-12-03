package streaming

import (
	"context"
	"sync"

	"github.com/containerd/containerd/v2/core/streaming"
	"github.com/containerd/containerd/v2/plugins"
	"github.com/containerd/errdefs"
	"github.com/containerd/plugin"
	"github.com/containerd/plugin/registry"
)

func init() {
	registry.Register(&plugin.Registration{
		Type:     plugins.StreamingPlugin,
		ID:       "manager",
		Requires: []plugin.Type{},
		InitFn: func(ic *plugin.InitContext) (interface{}, error) {
			sm := &streamManager{
				streams: map[string]*managedStream{},
			}
			return sm, nil
		},
	})
}

type streamManager struct {
	// streams maps name -> stream
	streams map[string]*managedStream

	rwlock sync.RWMutex
}

func (sm *streamManager) Register(ctx context.Context, name string, stream streaming.Stream) error {
	ms := &managedStream{
		Stream:  stream,
		name:    name,
		manager: sm,
	}

	sm.rwlock.Lock()
	defer sm.rwlock.Unlock()
	if _, ok := sm.streams[name]; ok {
		return errdefs.ErrAlreadyExists
	}
	sm.streams[name] = ms

	return nil
}

func (sm *streamManager) Get(ctx context.Context, name string) (streaming.Stream, error) {
	sm.rwlock.RLock()
	defer sm.rwlock.RUnlock()

	stream, ok := sm.streams[name]
	if !ok {
		return nil, errdefs.ErrNotFound
	}

	return stream, nil
}

type managedStream struct {
	streaming.Stream

	name    string
	manager *streamManager
}

func (m *managedStream) Close() error {
	m.manager.rwlock.Lock()
	delete(m.manager.streams, m.name)

	m.manager.rwlock.Unlock()
	return m.Stream.Close()
}
