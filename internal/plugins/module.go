package plugins

import (
	"bytes"
	"errors"
	"fmt"
)

// maxTableEntries bounds the function tables a module declares: wazero
// allocates them in full when instantiating, outside the memory limit.
// (A Go plugin has a few thousand entries.)
const maxTableEntries = 1 << 20

var errModule = errors.New("malformed WebAssembly module")

// checkModule reads the table declarations of a module (own and imported
// ones) and refuses oversized tables before anything is compiled.
func checkModule(b []byte) error {
	if !bytes.HasPrefix(b, wasmMagic) {
		return errModule
	}
	pos := len(wasmMagic)
	for pos < len(b) {
		id := b[pos]
		pos++
		size, n := uleb(b[pos:])
		if n == 0 || size > uint64(len(b)-pos-n) {
			return errModule
		}
		pos += n
		sec := b[pos : pos+int(size)]
		pos += int(size)
		var err error
		switch id {
		case 2:
			err = checkImports(sec)
		case 4:
			err = checkTables(sec)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func checkTables(sec []byte) error {
	count, n := uleb(sec)
	if n == 0 || count > 1000 {
		return errModule
	}
	r := sec[n:]
	for range count {
		if len(r) < 1 {
			return errModule
		}
		k, err := checkLimits(r[1:]) // after the reference type
		if err != nil {
			return err
		}
		r = r[1+k:]
	}
	return nil
}

func checkImports(sec []byte) error {
	count, n := uleb(sec)
	if n == 0 || count > 100000 {
		return errModule
	}
	r := sec[n:]
	skipName := func() bool {
		l, k := uleb(r)
		if k == 0 || l > uint64(len(r)-k) {
			return false
		}
		r = r[k+int(l):]
		return true
	}
	for range count {
		if !skipName() || !skipName() || len(r) < 1 {
			return errModule
		}
		kind := r[0]
		r = r[1:]
		switch kind {
		case 0: // function: type index
			_, k := uleb(r)
			if k == 0 {
				return errModule
			}
			r = r[k:]
		case 1: // table: reference type, limits
			if len(r) < 1 {
				return errModule
			}
			k, err := checkLimits(r[1:])
			if err != nil {
				return err
			}
			r = r[1+k:]
		case 2: // memory: limits (bounded by the runtime's page limit)
			k, err := skipLimits(r)
			if err != nil {
				return err
			}
			r = r[k:]
		case 3: // global: value type, mutability
			if len(r) < 2 {
				return errModule
			}
			r = r[2:]
		default:
			return errModule
		}
	}
	return nil
}

// checkLimits reads table limits and refuses large minimums; it returns
// the bytes read.
func checkLimits(b []byte) (int, error) {
	if len(b) < 1 {
		return 0, errModule
	}
	flag := b[0]
	min, n := uleb(b[1:])
	if n == 0 || flag > 1 {
		return 0, errModule
	}
	read := 1 + n
	if flag == 1 {
		_, m := uleb(b[read:])
		if m == 0 {
			return 0, errModule
		}
		read += m
	}
	if min > maxTableEntries {
		return 0, fmt.Errorf("the module declares a table of %d entries (at most %d)", min, maxTableEntries)
	}
	return read, nil
}

func skipLimits(b []byte) (int, error) {
	if len(b) < 1 {
		return 0, errModule
	}
	_, n := uleb(b[1:])
	if n == 0 {
		return 0, errModule
	}
	read := 1 + n
	if b[0]&1 == 1 {
		_, m := uleb(b[read:])
		if m == 0 {
			return 0, errModule
		}
		read += m
	}
	return read, nil
}

// uleb decodes an unsigned LEB128 number; n is 0 for invalid input.
func uleb(b []byte) (v uint64, n int) {
	for i := 0; i < len(b) && i < 10; i++ {
		v |= uint64(b[i]&0x7f) << (7 * i)
		if b[i]&0x80 == 0 {
			return v, i + 1
		}
	}
	return 0, 0
}
