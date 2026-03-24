// Package image provides image pipeline functionality for OCI/Docker, ISO, and raw disk images.
package image

import "fmt"

// Format represents an image format
type Format string

const (
	FormatUnknown Format = "unknown"
	FormatRaw     Format = "raw"
	FormatQCOW2   Format = "qcow2"
	FormatISO     Format = "iso"
	FormatVMDK    Format = "vmdk"
)

// String returns the string representation of Format
func (f Format) String() string {
	return string(f)
}

// PullOptions holds options for pulling an image
type PullOptions struct {
	// URL is the source URL for the image
	URL string
	// Format specifies the expected format
	Format Format
	// Username for authenticated pulls
	Username string
	// Password for authenticated pulls
	Password string
}

// ExtractOptions holds options for extracting an image
type ExtractOptions struct {
	// OutputPath specifies where to extract the image
	OutputPath string
	// Format specifies the input format (auto-detect if empty)
	Format Format
}

// PullPolicy controls when images are fetched from a remote registry.
type PullPolicy string

const (
	// PullMissing only pulls if the image is not already cached locally.
	PullMissing PullPolicy = "missing"
	// PullAlways checks the remote registry and re-pulls if the manifest digest changed.
	PullAlways PullPolicy = "always"
	// PullNever returns an error if the image is not cached locally.
	PullNever PullPolicy = "never"
)

// ValidatePullPolicy checks that the given string is a valid pull policy.
func ValidatePullPolicy(s string) (PullPolicy, error) {
	switch PullPolicy(s) {
	case PullMissing, PullAlways, PullNever:
		return PullPolicy(s), nil
	default:
		return "", fmt.Errorf("invalid pull policy %q: must be missing, always, or never", s)
	}
}
