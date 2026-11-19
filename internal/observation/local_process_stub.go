//go:build !linux && !darwin

package observation

// processTree is unavailable on platforms without a portable process
// enumerator; TreeSize stays zero so polling never judges quiescence from an
// unseen process tree.
func processTree(root int) ([]string, int, error) {
	return nil, 0, nil
}
