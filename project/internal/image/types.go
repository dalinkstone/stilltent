// Package image provides image pipeline functionality for OCI/Docker, ISO, and raw disk images.
package image

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
