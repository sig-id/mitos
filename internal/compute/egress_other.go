//go:build !linux

package compute

import "errors"

// loopbackUp is the non-Linux fallback for raising a namespace's loopback
// interface. A loopback that needs raising only occurs inside the Linux
// sandbox's network namespaces; elsewhere the interface is always up, so
// RunEgressLoop never reaches this path. Report the gap instead of pretending
// success.
func loopbackUp() error {
	return errors.New("raising loopback requires Linux netlink")
}
