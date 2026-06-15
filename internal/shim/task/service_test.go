//go:build linux

package task

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/pkg/shutdown"

	"github.com/spin-stack/spinbox/internal/host/network"
	"github.com/spin-stack/spinbox/internal/shim/lifecycle"
)

type mockShutdownService struct {
	callbacks []func(context.Context) error
	done      chan struct{}
	calls     atomic.Int32
	mu        sync.Mutex
}

func newMockShutdownService() *mockShutdownService {
	return &mockShutdownService{
		done: make(chan struct{}),
	}
}

func (m *mockShutdownService) Shutdown() {
	m.calls.Add(1)
}

func (m *mockShutdownService) RegisterCallback(fn func(context.Context) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callbacks = append(m.callbacks, fn)
}

func (m *mockShutdownService) Done() <-chan struct{} {
	return m.done
}

func (m *mockShutdownService) Err() error {
	select {
	case <-m.done:
		return shutdown.ErrShutdown
	default:
		return nil
	}
}

type mockNetworkManager struct {
	releaseCalls atomic.Int32
	closeCalls   atomic.Int32
}

func (m *mockNetworkManager) Close() error {
	m.closeCalls.Add(1)
	return nil
}

func (m *mockNetworkManager) EnsureNetworkResources(context.Context, *network.Environment) error {
	return nil
}

func (m *mockNetworkManager) ReleaseNetworkResources(context.Context, *network.Environment) error {
	m.releaseCalls.Add(1)
	return nil
}

func (m *mockNetworkManager) Metrics() *network.Metrics {
	return nil
}

func newTestService() *service {
	forwardDone := make(chan struct{})
	close(forwardDone)

	return &service{
		stateMachine:     lifecycle.NewStateMachine(),
		events:           make(chan any),
		eventsDone:       make(chan struct{}),
		forwardDone:      forwardDone,
		vmExitCh:         make(chan struct{}),
		vmLifecycle:      lifecycle.NewManager(),
		connManager:      NewConnectionManager(nil, nil),
		networkManager:   &mockNetworkManager{},
		exitFunc:         func(int) {},
		shutdownSvc:      newMockShutdownService(),
		initiateShutdown: nil,
	}
}

func waitForCondition(t *testing.T, timeout time.Duration, fn func() bool, msg string) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal(msg)
}

func TestBeginRPCRejectsDuringShutdown(t *testing.T) {
	s := newTestService()
	s.shutdownRequested.Store(true)

	done, err := s.beginRPC(false)
	if err == nil {
		t.Fatal("expected beginRPC to reject new RPCs during shutdown")
	}
	if done != nil {
		t.Fatal("expected nil completion function on rejected RPC")
	}
	if got := s.inflight.Load(); got != 0 {
		t.Fatalf("expected inflight to stay at 0, got %d", got)
	}

	done, err = s.beginRPC(true)
	if err != nil {
		t.Fatalf("expected beginRPC(true) to allow shutdown RPC, got %v", err)
	}
	done()
	if got := s.inflight.Load(); got != 0 {
		t.Fatalf("expected inflight to return to 0, got %d", got)
	}
}

func TestRequestShutdownAndExitWaitsForInflight(t *testing.T) {
	s := newTestService()
	shutdownSvc := newMockShutdownService()
	s.shutdownSvc = shutdownSvc

	exitCh := make(chan int, 1)
	s.exitFunc = func(code int) {
		exitCh <- code
	}

	s.inflight.Add(1)

	go s.requestShutdownAndExit(context.Background(), "test")

	select {
	case <-shutdownSvc.done:
		t.Fatal("shutdown should not complete before inflight requests drain")
	case <-time.After(100 * time.Millisecond):
	}

	if got := shutdownSvc.calls.Load(); got != 0 {
		t.Fatalf("expected shutdown callbacks not to start yet, got %d calls", got)
	}

	s.inflight.Add(-1)

	waitForCondition(t, time.Second, func() bool {
		return shutdownSvc.calls.Load() == 1
	}, "shutdown was not triggered after inflight drained")

	close(shutdownSvc.done)

	select {
	case code := <-exitCh:
		if code != 0 {
			t.Fatalf("expected exit code 0, got %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for shim exit")
	}
}

func TestCleanupOnDeleteFailureTriggersFullShutdown(t *testing.T) {
	s := newTestService()
	shutdownSvc := newMockShutdownService()
	s.shutdownSvc = shutdownSvc

	exitCh := make(chan int, 1)
	s.exitFunc = func(code int) {
		exitCh <- code
	}

	s.stateMachine.ForceTransition(lifecycle.StateDeleting)
	s.inflight.Add(1)

	s.cleanupOnDeleteFailure(context.Background(), "ctr")

	waitForCondition(t, time.Second, func() bool {
		return s.stateMachine.IsShuttingDown()
	}, "delete failure did not move shim to shutting down state")

	if !s.stateMachine.IsIntentionalShutdown() {
		t.Fatal("expected delete failure shutdown to be marked intentional")
	}

	if got := shutdownSvc.calls.Load(); got != 0 {
		t.Fatalf("expected shutdown to wait for inflight delete RPC, got %d calls", got)
	}

	s.inflight.Add(-1)

	waitForCondition(t, time.Second, func() bool {
		return shutdownSvc.calls.Load() == 1
	}, "shutdown was not triggered after delete failure")

	close(shutdownSvc.done)

	select {
	case <-exitCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for shim exit after delete failure")
	}
}

func TestRequestShutdownAndExitDoesNotReleaseNetworkInline(t *testing.T) {
	s := newTestService()
	shutdownSvc := newMockShutdownService()
	networkManager := &mockNetworkManager{}
	s.shutdownSvc = shutdownSvc
	s.networkManager = networkManager
	s.containerID = "ctr"

	exitCh := make(chan int, 1)
	s.exitFunc = func(code int) {
		exitCh <- code
	}

	go s.requestShutdownAndExit(context.Background(), "vm process exit")

	waitForCondition(t, time.Second, func() bool {
		return shutdownSvc.calls.Load() == 1
	}, "shutdown was not triggered")

	if got := networkManager.releaseCalls.Load(); got != 0 {
		t.Fatalf("expected no inline network cleanup before shutdown callbacks, got %d releases", got)
	}

	close(shutdownSvc.done)

	select {
	case <-exitCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for shim exit")
	}
}

func TestRequestShutdownAndExitWaitsForForwarder(t *testing.T) {
	s := newTestService()
	shutdownSvc := newMockShutdownService()
	s.shutdownSvc = shutdownSvc
	s.forwardDone = make(chan struct{})

	exitCh := make(chan int, 1)
	s.exitFunc = func(code int) {
		exitCh <- code
	}

	go s.requestShutdownAndExit(context.Background(), "test")

	waitForCondition(t, time.Second, func() bool {
		return shutdownSvc.calls.Load() == 1
	}, "shutdown was not triggered")

	close(shutdownSvc.done)

	select {
	case <-exitCh:
		t.Fatal("exit should wait for forwarder completion")
	case <-time.After(100 * time.Millisecond):
	}

	close(s.forwardDone)

	select {
	case <-exitCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for exit after forwarder completion")
	}
}

func TestShutdownReleasesNetworkOnce(t *testing.T) {
	s := newTestService()
	networkManager := &mockNetworkManager{}
	s.networkManager = networkManager
	s.containerID = "ctr"
	s.container = &container{}

	if err := s.shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown returned error: %v", err)
	}

	if got := networkManager.releaseCalls.Load(); got != 1 {
		t.Fatalf("expected one network release during shutdown, got %d", got)
	}
	if got := networkManager.closeCalls.Load(); got != 1 {
		t.Fatalf("expected network manager close to be called once, got %d", got)
	}
	if !s.stateMachine.IsShuttingDown() {
		t.Fatal("expected shim state to be shutting down")
	}

	select {
	case <-s.eventsDone:
	default:
		t.Fatal("expected eventsDone to be closed during shutdown")
	}
}
