package process

import (
	google_protobuf "github.com/containerd/containerd/v2/pkg/protobuf/types"
)

// Mount holds filesystem mount configuration
type Mount struct {
	Type    string
	Source  string
	Target  string
	Options []string
}

// CreateConfig hold task creation configuration
type CreateConfig struct {
	ID               string
	Bundle           string
	Runtime          string
	Rootfs           []Mount
	Terminal         bool
	Stdin            string
	Stdout           string
	Stderr           string
	Checkpoint       string
	ParentCheckpoint string
	Options          *google_protobuf.Any
}

// ExecConfig holds exec creation configuration
type ExecConfig struct {
	ID       string
	Terminal bool
	Stdin    string
	Stdout   string
	Stderr   string
	Spec     *google_protobuf.Any
}

// CheckpointConfig holds task checkpoint configuration
type CheckpointConfig struct {
	WorkDir                  string
	Path                     string
	Exit                     bool
	AllowOpenTCP             bool
	AllowExternalUnixSockets bool
	AllowTerminal            bool
	FileLocks                bool
	EmptyNamespaces          []string
}
