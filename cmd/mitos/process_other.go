//go:build !linux

package main

import (
	"errors"
	"os"
)

func signalStoreServer(*os.File) error { return errors.New("mitos stop currently requires Linux") }
