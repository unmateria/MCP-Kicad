//go:build !libavoid

package route2

// libavoidAvailable returns false on builds without the `libavoid` tag. The
// real cgo bindings live in libavoid/libavoid.go and are guarded by the
// inverse build tag. Until the vendored C++ source is added, the public
// Router interface always returns the astarpp implementation.
func libavoidAvailable() bool { return false }
