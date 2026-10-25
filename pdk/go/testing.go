//go:build !wasip1

package gopress

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Outside WebAssembly (go test), plugins run against a fake host: tests
// install it with SetTestHost and call the registered handlers with
// CallExport.

var testHost func(op string, args json.RawMessage) (any, error)

// SetTestHost installs the function that answers host calls in tests:
// it gets the operation and its JSON arguments and returns the result
// (encoded as JSON) or an error.
func SetTestHost(fn func(op string, args json.RawMessage) (any, error)) { testHost = fn }

func hostRequest(req []byte) ([]byte, error) {
	if testHost == nil {
		return nil, errors.New("no test host (SetTestHost)")
	}
	var r struct {
		Op   string          `json:"op"`
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(req, &r); err != nil {
		return nil, err
	}
	result, err := testHost(r.Op, r.Args)
	if err != nil {
		return json.Marshal(map[string]string{"error": err.Error()})
	}
	return json.Marshal(map[string]any{"result": result})
}

// CallExport runs a registered handler like the host would: in is encoded
// as the export's input and the output is decoded into out (may be nil).
func CallExport(export string, in, out any) error {
	fn := exports[export]
	if fn == nil {
		return fmt.Errorf("unknown export %q", export)
	}
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	res, err := fn(b)
	if err != nil {
		return err
	}
	if out != nil {
		return json.Unmarshal(res, out)
	}
	return nil
}
