//go:build !(linux || darwin)

package store

import "errors"

// diskFree is unsupported here; the store then skips the free-space guard.
func diskFree(string) (int64, error) { return 0, errors.ErrUnsupported }
