//go:build !windows

package stt

import "os"

func superviseProcessTree(_ *os.Process) (func(), error) {
	return func() {}, nil
}
