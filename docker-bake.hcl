# ============================================================================
# Build Configuration Variables
# ============================================================================

# Kernel configuration
variable "KERNEL_VERSION" {
  default = "6.18"
}

variable "KERNEL_ARCH" {
  default = "x86_64"
}

variable "KERNEL_NPROC" {
  default = "8"
}

# Go build configuration
variable "GO_BUILD_FLAGS" {
  default = ""
}

variable "GO_GCFLAGS" {
  default = ""
}

variable "GO_DEBUG_GCFLAGS" {
  default = ""
}

variable "GO_LDFLAGS" {
  default = ""
}

# Linting configuration
variable "GOLANGCI_LINT_MULTIPLATFORM" {
  default = ""
}

# ============================================================================
# Common Build Configuration
# ============================================================================

# Shared build arguments for all targets
target "_common" {
  args = {
    KERNEL_VERSION = KERNEL_VERSION
    KERNEL_ARCH = KERNEL_ARCH
    KERNEL_NPROC = KERNEL_NPROC
    GO_BUILD_FLAGS = GO_BUILD_FLAGS
    GO_GCFLAGS = GO_GCFLAGS
    GO_DEBUG_GCFLAGS = GO_DEBUG_GCFLAGS
    GO_LDFLAGS = GO_LDFLAGS
  }
}

# Output directory for build artifacts
variable "DESTDIR" {
  default = "_output"
}

# ============================================================================
# Build Targets
# ============================================================================

# Interactive kernel menuconfig (for adjusting kernel configuration)
target "menuconfig" {
  inherits = ["_common"]
  target = "kernel-build-base"
  output = ["type=image,name=beacon-menuconfig"]
}

# Build Linux kernel
target "kernel" {
  inherits = ["_common"]
  target = "kernel"
  platforms = ["linux/amd64"]
  output = ["${DESTDIR}"]
}

# Build initrd with vminitd and crun
target "initrd" {
  inherits = ["_common"]
  target = "initrd"
  platforms = ["linux/amd64"]
  output = ["${DESTDIR}"]
}

# Build containerd shim
target "shim" {
  inherits = ["_common"]
  target = "shim"
  platforms = ["linux/amd64"]
  output = ["${DESTDIR}"]
}

# Build all artifacts (default target)
group "default" {
  targets = ["kernel", "initrd", "shim"]
}

# Development environment with containerd, docker CLI, and delve
target "dev" {
  inherits = ["_common"]
  target = "dev"
  output = ["type=image,name=beacon-dev"]
}

# ============================================================================
# Validation and Linting Targets
# ============================================================================

# Run all validation checks
group "validate" {
  targets = ["lint", "validate-dockerfile"]
}

# Run linting (golangci-lint, yamllint, golangci config verify)
target "lint" {
  name = "lint-${build.name}"
  inherits = ["_common"]
  output = ["type=cacheonly"]
  target = build.target
  args = {
    TARGETNAME = build.name
    GOLANGCI_FROM_SOURCE = "true"
  }
  # Enable multi-platform linting for golangci-lint when requested
  platforms = (build.target == "golangci-lint") && (GOLANGCI_LINT_MULTIPLATFORM != null) ? [
    "linux/amd64",
    "darwin/arm64",
  ] : []
  matrix = {
    build = [
      {
        name = "default",
        target = "golangci-lint",
      },
      {
        name = "golangci-verify",
        target = "golangci-verify",
      },
      {
        name = "yaml",
        target = "yamllint",
      },
    ]
  }
}

# Validate Dockerfile syntax
target "validate-dockerfile" {
  matrix = {
    dockerfile = ["Dockerfile"]
  }
  name = "validate-dockerfile-${md5(dockerfile)}"
  inherits = ["_common"]
  dockerfile = dockerfile
  call = "check"
}
