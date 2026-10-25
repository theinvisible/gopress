//go:build wasip1

package gopress

import (
	"errors"

	"github.com/extism/go-pdk"
)

//go:wasmimport extism:host/user gopress
func hostCall(uint64) uint64

// hostRequest sends a JSON request through the host function.
func hostRequest(req []byte) ([]byte, error) {
	mem := pdk.AllocateBytes(req)
	defer mem.Free()
	off := hostCall(mem.Offset())
	if off == 0 {
		return nil, errors.New("host call failed")
	}
	rm := pdk.FindMemory(off)
	b := rm.ReadBytes()
	rm.Free()
	return b, nil
}
