package services

import (
	"errors"

	"github.com/containerd/errdefs"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// isErrType checks if error matches expected type.
// Helper to check if error matches expected type by comparing both direct errors
// and gRPC status codes (since containerd errors are converted to gRPC codes).
func isErrType(err error, want error) bool {
	// First try direct comparison
	if errors.Is(err, want) {
		return true
	}

	// Then check gRPC status code
	st, ok := status.FromError(err)
	if !ok {
		return false
	}

	// Map containerd errors to gRPC codes
	var expectedCode codes.Code
	switch want {
	case errdefs.ErrInvalidArgument:
		expectedCode = codes.InvalidArgument
	case errdefs.ErrNotFound:
		expectedCode = codes.NotFound
	case errdefs.ErrAlreadyExists:
		expectedCode = codes.AlreadyExists
	case errdefs.ErrFailedPrecondition:
		expectedCode = codes.FailedPrecondition
	default:
		return false
	}

	return st.Code() == expectedCode
}
