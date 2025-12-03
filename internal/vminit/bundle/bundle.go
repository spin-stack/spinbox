// Package bundle defines constants for VM bundle management
package bundle

const (
	// RootDir is the directory where container bundles are stored inside the VM.
	// Each container gets a subdirectory: /run/bundles/{container-id}/
	RootDir = "/run/bundles"
)
