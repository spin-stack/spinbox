package boltstore

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
)

// InMemoryStore provides an in-memory implementation of Store[T] for testing
type InMemoryStore[T any] struct {
	mu   sync.RWMutex
	data map[string][]byte
}

// NewInMemoryStore creates a new in-memory store
func NewInMemoryStore[T any]() Store[T] {
	return &InMemoryStore[T]{
		data: make(map[string][]byte),
	}
}

// Get retrieves a value by key
func (s *InMemoryStore[T]) Get(ctx context.Context, key string) (*T, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, ok := s.data[key]
	if !ok {
		return nil, ErrNotFound
	}

	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	return &value, nil
}

// Set stores a value by key
func (s *InMemoryStore[T]) Set(ctx context.Context, key string, value *T) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = data
	return nil
}

// Delete removes a value by key
func (s *InMemoryStore[T]) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

// Scan iterates over all keys with the given prefix
func (s *InMemoryStore[T]) Scan(ctx context.Context, prefix string, fn func(key string, value *T) error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for k, data := range s.data {
		if !strings.HasPrefix(k, prefix) {
			continue
		}

		var value T
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		if err := fn(k, &value); err != nil {
			return err
		}
	}
	return nil
}

// Close is a no-op for in-memory store
func (s *InMemoryStore[T]) Close() error {
	return nil
}
