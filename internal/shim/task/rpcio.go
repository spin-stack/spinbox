package task

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"syscall"
	"time"

	"github.com/containerd/containerd/v2/pkg/stdio"
	"github.com/containerd/fifo"
	"github.com/containerd/log"
	"github.com/containerd/ttrpc"

	stdiov1 "github.com/aledbf/qemubox/containerd/api/services/stdio/v1"
	"github.com/aledbf/qemubox/containerd/internal/iobuf"
)

const (
	// outputRetryInitialDelay is the initial delay for output forwarding retry.
	outputRetryInitialDelay = 100 * time.Millisecond
	// outputRetryMaxDelay is the maximum delay between retries.
	outputRetryMaxDelay = 2 * time.Second
	// outputRetryMaxAttempts is the maximum number of retry attempts before giving up.
	// 0 means retry indefinitely until context is cancelled or done is signaled.
	outputRetryMaxAttempts = 0
)

// ClientDialer is a function type for dialing the guest TTRPC client.
type ClientDialer func(ctx context.Context) (*ttrpc.Client, error)

// RPCIOForwarder forwards I/O between host FIFOs and guest process via TTRPC.
// It supports multiple attach operations by maintaining persistent RPC connections.
type RPCIOForwarder struct {
	dialClient  ClientDialer
	containerID string
	execID      string

	stdinPath  string
	stdoutPath string
	stderrPath string

	mu      sync.Mutex
	started bool
	done    chan struct{}
	wg      sync.WaitGroup

	// ctx is a long-lived context for the forwarder goroutines, tied to container lifecycle
	ctx    context.Context
	cancel context.CancelFunc

	// stdinCloser is used to signal stdin closure
	stdinCloser chan struct{}

	// clients holds TTRPC clients to be closed on shutdown
	clients   []*ttrpc.Client
	clientsMu sync.Mutex
}

// NewRPCIOForwarder creates a new RPC I/O forwarder.
func NewRPCIOForwarder(dialClient ClientDialer, containerID, execID string, sio stdio.Stdio) *RPCIOForwarder {
	return &RPCIOForwarder{
		dialClient:  dialClient,
		containerID: containerID,
		execID:      execID,
		stdinPath:   sio.Stdin,
		stdoutPath:  sio.Stdout,
		stderrPath:  sio.Stderr,
		done:        make(chan struct{}),
		stdinCloser: make(chan struct{}),
	}
}

// Start begins forwarding I/O. This should be called after the guest process is created.
// The passed context is used to copy log fields, but the forwarder creates its own
// long-lived context that survives until Shutdown is called.
func (f *RPCIOForwarder) Start(ctx context.Context) error {
	f.mu.Lock()
	if f.started {
		f.mu.Unlock()
		return nil
	}
	f.started = true

	// Create a long-lived context for the forwarder goroutines.
	// This context is NOT tied to the RPC request context - it lives until Shutdown.
	// We use context.WithoutCancel to preserve log fields but detach from parent cancellation.
	f.ctx, f.cancel = context.WithCancel(context.WithoutCancel(ctx))
	f.mu.Unlock()

	log.G(f.ctx).WithFields(log.Fields{
		"container": f.containerID,
		"exec":      f.execID,
		"stdin":     f.stdinPath,
		"stdout":    f.stdoutPath,
		"stderr":    f.stderrPath,
	}).Debug("starting RPC I/O forwarder")

	// Start stdin forwarder if stdin is configured
	if f.stdinPath != "" {
		f.wg.Add(1)
		go f.forwardStdin(f.ctx)
	}

	// Start stdout forwarder if stdout is configured
	if f.stdoutPath != "" {
		f.wg.Add(1)
		go f.forwardStdout(f.ctx)
	}

	// Start stderr forwarder if stderr is configured and different from stdout
	if f.stderrPath != "" && f.stderrPath != f.stdoutPath {
		f.wg.Add(1)
		go f.forwardStderr(f.ctx)
	}

	return nil
}

// forwardStdin reads from host FIFO and writes to guest via WriteStdin RPC.
// It handles reconnection when the writer (ctr attach) disconnects and reconnects.
func (f *RPCIOForwarder) forwardStdin(ctx context.Context) {
	defer f.wg.Done()

	logger := log.G(ctx).WithField("container", f.containerID).WithField("direction", "stdin")
	logger.Debug("starting stdin forwarder")

	buf := iobuf.Get()
	defer iobuf.Put(buf)

	// Get RPC client once
	client, err := f.getStdIOClient(ctx)
	if err != nil {
		logger.WithError(err).Error("failed to get stdio client")
		return
	}

	// Loop to handle FIFO reconnection (when attach disconnects and reconnects)
	for {
		select {
		case <-ctx.Done():
			logger.Debug("stdin forwarder context done")
			return
		case <-f.done:
			logger.Debug("stdin forwarder done")
			return
		case <-f.stdinCloser:
			logger.Debug("stdin closed by user")
			_, _ = client.CloseStdin(ctx, &stdiov1.CloseStdinRequest{
				ContainerId: f.containerID,
				ExecId:      f.execID,
			})
			return
		default:
		}

		// Open FIFO for reading - blocks until a writer connects
		logger.Debug("opening stdin fifo, waiting for writer")
		fifoReader, err := fifo.OpenFifo(ctx, f.stdinPath, syscall.O_RDONLY, 0)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				logger.Debug("stdin fifo open cancelled")
				return
			}
			logger.WithError(err).Error("failed to open stdin fifo")
			return
		}

		logger.Debug("stdin fifo opened, forwarding data")

		// Forward data until EOF or error
		// Use a flag to break out of the read loop on EOF (break in select only breaks select, not for)
		gotEOF := false
		for !gotEOF {
			select {
			case <-ctx.Done():
				_ = fifoReader.Close()
				logger.Debug("stdin forwarder context done during read")
				return
			case <-f.done:
				_ = fifoReader.Close()
				logger.Debug("stdin forwarder done during read")
				return
			case <-f.stdinCloser:
				_ = fifoReader.Close()
				logger.Debug("stdin closed by user during read")
				_, _ = client.CloseStdin(ctx, &stdiov1.CloseStdinRequest{
					ContainerId: f.containerID,
					ExecId:      f.execID,
				})
				return
			default:
				n, err := fifoReader.Read(*buf)
				if err != nil {
					_ = fifoReader.Close()
					if errors.Is(err, io.EOF) {
						// Writer disconnected - this is normal for detached containers
						// or when attach exits. Reopen FIFO to wait for next writer.
						logger.Debug("stdin EOF - writer disconnected, waiting for reconnect")
						gotEOF = true // Exit inner loop, continue outer loop to reopen FIFO
						continue
					}
					if !isClosedConnError(err) {
						logger.WithError(err).Warn("error reading from stdin fifo")
					}
					return
				}

				if n > 0 {
					logger.WithField("bytes", n).Debug("forwarding stdin data to guest")
					_, err = client.WriteStdin(ctx, &stdiov1.WriteStdinRequest{
						ContainerId: f.containerID,
						ExecId:      f.execID,
						Data:        (*buf)[:n],
					})
					if err != nil {
						_ = fifoReader.Close()
						logger.WithError(err).Warn("error writing to stdin RPC")
						return
					}
				}
			}
		}
	}
}

// forwardStdout reads from guest via ReadStdout RPC and writes to host FIFO.
func (f *RPCIOForwarder) forwardStdout(ctx context.Context) {
	defer f.wg.Done()
	f.forwardOutput(ctx, "stdout", f.stdoutPath)
}

// forwardStderr reads from guest via ReadStderr RPC and writes to host FIFO.
func (f *RPCIOForwarder) forwardStderr(ctx context.Context) {
	defer f.wg.Done()
	f.forwardOutput(ctx, "stderr", f.stderrPath)
}

func (f *RPCIOForwarder) forwardOutput(ctx context.Context, streamName, path string) {
	logger := log.G(ctx).WithField("container", f.containerID).WithField("direction", streamName)
	logger.Debug("starting output forwarder")

	// Open FIFO for reading first to prevent O_WRONLY from blocking.
	// This "dummy reader" keeps the FIFO open so writes don't fail with SIGPIPE
	// when there's no real reader (e.g., before ctr attach connects).
	// We use O_RDONLY|O_NONBLOCK to avoid blocking if no writer exists yet.
	logger.WithField("path", path).Debug("opening output fifo for reading (dummy reader)")
	fifoReader, err := fifo.OpenFifo(ctx, path, syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		logger.WithError(err).Error("failed to open output fifo for reading")
		return
	}
	defer func() { _ = fifoReader.Close() }()
	logger.Debug("opened output fifo for reading")

	// Now open for writing - won't block because we have a reader above
	logger.Debug("opening output fifo for writing")
	fifoWriter, err := fifo.OpenFifo(ctx, path, syscall.O_WRONLY, 0)
	if err != nil {
		logger.WithError(err).Error("failed to open output fifo")
		return
	}
	defer func() { _ = fifoWriter.Close() }()
	logger.Debug("opened output fifo for writing")

	// Retry loop for connection and streaming
	retryDelay := outputRetryInitialDelay
	attempts := 0

	for {
		// Check for shutdown before each attempt
		select {
		case <-ctx.Done():
			logger.Debug("output forwarder context done")
			return
		case <-f.done:
			logger.Debug("output forwarder done")
			return
		default:
		}

		// Get RPC client
		logger.Debug("getting stdio RPC client")
		client, err := f.getStdIOClient(ctx)
		if err != nil {
			attempts++
			if outputRetryMaxAttempts > 0 && attempts >= outputRetryMaxAttempts {
				logger.WithError(err).WithField("attempts", attempts).Error("failed to get stdio client after max retries")
				return
			}
			logger.WithError(err).WithField("retryDelay", retryDelay).Debug("failed to get stdio client, will retry")
			if !f.sleepWithCancel(ctx, retryDelay) {
				return
			}
			retryDelay = f.nextRetryDelay(retryDelay)
			continue
		}
		logger.Debug("got stdio RPC client")

		// Start streaming from guest
		var stream stdiov1.StdIO_ReadStdoutClient
		req := &stdiov1.ReadOutputRequest{
			ContainerId: f.containerID,
			ExecId:      f.execID,
		}

		logger.Debug("starting RPC output stream")
		if streamName == "stdout" {
			stream, err = client.ReadStdout(ctx, req)
		} else {
			stream, err = client.ReadStderr(ctx, req)
		}
		if err != nil {
			attempts++
			if outputRetryMaxAttempts > 0 && attempts >= outputRetryMaxAttempts {
				logger.WithError(err).WithField("attempts", attempts).Error("failed to start output stream after max retries")
				return
			}
			logger.WithError(err).WithField("retryDelay", retryDelay).Debug("failed to start output stream, will retry")
			if !f.sleepWithCancel(ctx, retryDelay) {
				return
			}
			retryDelay = f.nextRetryDelay(retryDelay)
			continue
		}
		logger.Debug("RPC output stream started, waiting for data")

		// Reset retry state on successful connection
		retryDelay = outputRetryInitialDelay
		attempts = 0

		// Stream data until error or EOF
		streamErr := f.streamOutput(ctx, logger, stream, fifoWriter)
		if streamErr == nil {
			// Clean EOF, we're done
			return
		}

		// Check if this is a terminal error or if we should retry
		if errors.Is(streamErr, context.Canceled) || errors.Is(streamErr, context.DeadlineExceeded) {
			return
		}

		// For other errors, retry with backoff
		attempts++
		if outputRetryMaxAttempts > 0 && attempts >= outputRetryMaxAttempts {
			logger.WithError(streamErr).WithField("attempts", attempts).Error("output stream failed after max retries")
			return
		}
		logger.WithError(streamErr).WithField("retryDelay", retryDelay).Debug("output stream error, will retry")
		if !f.sleepWithCancel(ctx, retryDelay) {
			return
		}
		retryDelay = f.nextRetryDelay(retryDelay)
	}
}

// streamOutput reads from the RPC stream and writes to the FIFO.
// Returns nil on clean EOF, or an error if the stream should be retried.
func (f *RPCIOForwarder) streamOutput(ctx context.Context, logger *log.Entry, stream stdiov1.StdIO_ReadStdoutClient, fifoWriter io.Writer) error {
	for {
		select {
		case <-ctx.Done():
			logger.Debug("output forwarder context done")
			return nil
		case <-f.done:
			logger.Debug("output forwarder done")
			return nil
		default:
			chunk, err := stream.Recv()
			if err != nil {
				if errors.Is(err, io.EOF) {
					logger.Debug("output stream EOF")
					return nil
				}
				if isClosedConnError(err) {
					// Connection closed - could be transient, return error for retry
					return err
				}
				logger.WithError(err).Warn("error receiving from output stream")
				return err
			}

			if chunk.Eof {
				logger.Debug("output chunk EOF")
				return nil
			}

			if len(chunk.Data) > 0 {
				logger.WithField("bytes", len(chunk.Data)).Debug("received data from guest, writing to fifo")
				_, err = fifoWriter.Write(chunk.Data)
				if err != nil {
					logger.WithError(err).Warn("error writing to output fifo")
					// FIFO write errors are terminal - don't retry
					return nil
				}
			}
		}
	}
}

// sleepWithCancel sleeps for the given duration, returning false if cancelled.
func (f *RPCIOForwarder) sleepWithCancel(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-f.done:
		return false
	case <-timer.C:
		return true
	}
}

// nextRetryDelay calculates the next retry delay with exponential backoff.
func (f *RPCIOForwarder) nextRetryDelay(current time.Duration) time.Duration {
	next := current * 2
	if next > outputRetryMaxDelay {
		next = outputRetryMaxDelay
	}
	return next
}

// getStdIOClient returns a StdIO client connected to the guest.
// The underlying TTRPC connection is tracked and will be closed on Shutdown.
func (f *RPCIOForwarder) getStdIOClient(ctx context.Context) (stdiov1.StdIOClient, error) {
	conn, err := f.dialClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to dial guest: %w", err)
	}

	// Track the client for cleanup on Shutdown
	f.clientsMu.Lock()
	f.clients = append(f.clients, conn)
	f.clientsMu.Unlock()

	return stdiov1.NewStdIOClient(conn), nil
}

// CloseStdin signals that stdin should be closed.
func (f *RPCIOForwarder) CloseStdin() {
	select {
	case f.stdinCloser <- struct{}{}:
	default:
	}
}

// Shutdown stops all I/O forwarding and cleans up resources.
func (f *RPCIOForwarder) Shutdown(ctx context.Context) error {
	f.mu.Lock()
	if !f.started {
		f.mu.Unlock()
		return nil
	}
	f.mu.Unlock()

	log.G(ctx).WithField("container", f.containerID).Debug("shutting down RPC I/O forwarder")

	// Cancel the forwarder context to stop all goroutines
	if f.cancel != nil {
		f.cancel()
	}

	// Signal done and wait for goroutines to finish
	close(f.done)
	f.wg.Wait()

	// Close all tracked TTRPC clients
	f.clientsMu.Lock()
	for _, client := range f.clients {
		if err := client.Close(); err != nil {
			log.G(ctx).WithError(err).Debug("error closing stdio TTRPC client")
		}
	}
	f.clients = nil
	f.clientsMu.Unlock()

	return nil
}

// GuestStdio returns the stdio configuration to pass to the guest.
// This uses the rpcio:// scheme to indicate RPC-based I/O.
func (f *RPCIOForwarder) GuestStdio() stdio.Stdio {
	var stdin, stdout, stderr string
	if f.stdinPath != "" {
		stdin = fmt.Sprintf("rpcio://%s/%s", f.containerID, f.execID)
	}
	if f.stdoutPath != "" {
		stdout = fmt.Sprintf("rpcio://%s/%s", f.containerID, f.execID)
	}
	if f.stderrPath != "" {
		stderr = fmt.Sprintf("rpcio://%s/%s", f.containerID, f.execID)
	}
	return stdio.Stdio{
		Stdin:    stdin,
		Stdout:   stdout,
		Stderr:   stderr,
		Terminal: false,
	}
}

// forwardIORPC sets up RPC-based I/O forwarding for non-TTY mode.
// Returns the guest stdio config, a start function (must be called after guest Create),
// shutdown function, and the forwarder itself (for CloseIO handling).
func (s *service) forwardIORPC(ctx context.Context, containerID, execID string, sio stdio.Stdio) (stdio.Stdio, func(context.Context) error, func(context.Context) error, *RPCIOForwarder, error) {
	forwarder := NewRPCIOForwarder(s.vmLifecycle.DialClient, containerID, execID, sio)

	guestStdio := forwarder.GuestStdio()

	log.G(ctx).WithFields(log.Fields{
		"container":   containerID,
		"exec":        execID,
		"guestStdin":  guestStdio.Stdin,
		"guestStdout": guestStdio.Stdout,
		"guestStderr": guestStdio.Stderr,
	}).Debug("RPC I/O forwarder created (not yet started)")

	// Return a start function that must be called AFTER the guest has created the process.
	// This ensures the I/O manager has registered the process before we try to subscribe.
	startFunc := func(ctx context.Context) error {
		return forwarder.Start(ctx)
	}

	return guestStdio, startFunc, forwarder.Shutdown, forwarder, nil
}
