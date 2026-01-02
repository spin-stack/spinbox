package stdio

import (
	"context"
	"errors"
	"io"

	"github.com/containerd/errdefs"
	"github.com/containerd/errdefs/pkg/errgrpc"
	"github.com/containerd/log"
	"github.com/containerd/ttrpc"

	stdiov1 "github.com/aledbf/qemubox/containerd/api/services/stdio/v1"
)

// service implements the StdIOService TTRPC interface.
type service struct {
	manager *Manager
}

// NewService creates a new StdIO service backed by the given manager.
func NewService(manager *Manager) *service {
	return &service{manager: manager}
}

// RegisterTTRPC registers the service with a TTRPC server.
func (s *service) RegisterTTRPC(server *ttrpc.Server) error {
	stdiov1.RegisterStdIOService(server, s)
	return nil
}

// WriteStdin writes data to a process's stdin.
func (s *service) WriteStdin(ctx context.Context, req *stdiov1.WriteStdinRequest) (*stdiov1.WriteStdinResponse, error) {
	log.G(ctx).WithField("container", req.ContainerId).WithField("exec", req.ExecId).WithField("len", len(req.Data)).Debug("WriteStdin")

	n, err := s.manager.WriteStdin(req.ContainerId, req.ExecId, req.Data)
	if err != nil {
		return nil, toGRPCError(err)
	}

	return &stdiov1.WriteStdinResponse{
		BytesWritten: uint32(n),
	}, nil
}

// ReadStdout streams stdout data from a process.
func (s *service) ReadStdout(ctx context.Context, req *stdiov1.ReadOutputRequest, stream stdiov1.StdIO_ReadStdoutServer) error {
	log.G(ctx).WithField("container", req.ContainerId).WithField("exec", req.ExecId).Debug("ReadStdout started")

	ch, err := s.manager.SubscribeStdout(ctx, req.ContainerId, req.ExecId)
	if err != nil {
		return toGRPCError(err)
	}

	return s.streamOutput(ctx, ch, stream, "stdout", req.ContainerId, req.ExecId)
}

// ReadStderr streams stderr data from a process.
func (s *service) ReadStderr(ctx context.Context, req *stdiov1.ReadOutputRequest, stream stdiov1.StdIO_ReadStderrServer) error {
	log.G(ctx).WithField("container", req.ContainerId).WithField("exec", req.ExecId).Debug("ReadStderr started")

	ch, err := s.manager.SubscribeStderr(ctx, req.ContainerId, req.ExecId)
	if err != nil {
		return toGRPCError(err)
	}

	return s.streamOutput(ctx, ch, stream, "stderr", req.ContainerId, req.ExecId)
}

// outputSender abstracts the Send method for both stdout and stderr streams.
type outputSender interface {
	Send(*stdiov1.OutputChunk) error
}

func (s *service) streamOutput(ctx context.Context, ch <-chan OutputData, stream outputSender, streamName, containerID, execID string) error {
	for {
		select {
		case <-ctx.Done():
			log.G(ctx).WithField("container", containerID).WithField("stream", streamName).Debug("stream cancelled")
			return ctx.Err()

		case data, ok := <-ch:
			if !ok {
				// Channel closed, send EOF.
				log.G(ctx).WithField("container", containerID).WithField("stream", streamName).Debug("stream channel closed")
				return stream.Send(&stdiov1.OutputChunk{Eof: true})
			}

			log.G(ctx).WithField("container", containerID).WithField("stream", streamName).
				WithField("bytes", len(data.Data)).WithField("eof", data.EOF).Debug("received chunk from channel")

			chunk := &stdiov1.OutputChunk{
				Data: data.Data,
				Eof:  data.EOF,
			}

			if err := stream.Send(chunk); err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				log.G(ctx).WithError(err).WithField("container", containerID).WithField("stream", streamName).Warn("error sending chunk")
				return err
			}

			if data.EOF {
				log.G(ctx).WithField("container", containerID).WithField("stream", streamName).Debug("stream EOF")
				return nil
			}
		}
	}
}

// CloseStdin closes a process's stdin.
func (s *service) CloseStdin(ctx context.Context, req *stdiov1.CloseStdinRequest) (*stdiov1.CloseStdinResponse, error) {
	log.G(ctx).WithField("container", req.ContainerId).WithField("exec", req.ExecId).Debug("CloseStdin")

	if err := s.manager.CloseStdin(req.ContainerId, req.ExecId); err != nil {
		return nil, toGRPCError(err)
	}

	return &stdiov1.CloseStdinResponse{}, nil
}

// toGRPCError converts an error to a GRPC-compatible error.
func toGRPCError(err error) error {
	if errdefs.IsNotFound(err) {
		return errgrpc.ToGRPC(err)
	}
	if errdefs.IsFailedPrecondition(err) {
		return errgrpc.ToGRPC(err)
	}
	return err
}
