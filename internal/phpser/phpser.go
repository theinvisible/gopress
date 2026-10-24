// Package phpser decodes PHP serialize() output as found in WordPress
// options and meta values.
//
// Mapping: N → nil, b → bool, i → int64, d → float64, s → string,
// a → []any if the keys are exactly 0..n-1 in order, else map[string]any
// (integer keys formatted in decimal), O → map[string]any with the class name
// under "__class".
package phpser

import (
	"fmt"
	"strconv"
	"strings"
)

// Unmarshal decodes a single serialized PHP value.
func Unmarshal(s string) (any, error) {
	d := decoder{s: s}
	v, err := d.value()
	if err != nil {
		return nil, err
	}
	if d.pos != len(d.s) {
		return nil, d.errorf("trailing data")
	}
	return v, nil
}

// IsSerialized reports whether s looks like serialized PHP data, like
// WordPress' is_serialized().
func IsSerialized(s string) bool {
	s = strings.TrimSpace(s)
	if s == "N;" {
		return true
	}
	if len(s) < 4 || s[1] != ':' {
		return false
	}
	switch s[0] {
	case 'a', 'O', 's', 'i', 'd', 'b':
		last := s[len(s)-1]
		return last == ';' || last == '}'
	}
	return false
}

// Maybe decodes s if it is serialized and returns it unchanged otherwise,
// like WordPress' maybe_unserialize().
func Maybe(s string) any {
	if !IsSerialized(s) {
		return s
	}
	v, err := Unmarshal(s)
	if err != nil {
		return s
	}
	return v
}

type decoder struct {
	s   string
	pos int
}

func (d *decoder) errorf(format string, args ...any) error {
	return fmt.Errorf("phpser: offset %d: %s", d.pos, fmt.Sprintf(format, args...))
}

func (d *decoder) expect(b byte) error {
	if d.pos >= len(d.s) || d.s[d.pos] != b {
		return d.errorf("expected %q", b)
	}
	d.pos++
	return nil
}

// until returns the text up to (not including) the next b and skips b.
func (d *decoder) until(b byte) (string, error) {
	i := strings.IndexByte(d.s[d.pos:], b)
	if i < 0 {
		return "", d.errorf("missing %q", b)
	}
	tok := d.s[d.pos : d.pos+i]
	d.pos += i + 1
	return tok, nil
}

func (d *decoder) int(term byte) (int64, error) {
	tok, err := d.until(term)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(tok, 10, 64)
	if err != nil {
		return 0, d.errorf("bad integer %q", tok)
	}
	return n, nil
}

// str reads `len:"bytes"` (the part after "s:").
func (d *decoder) str() (string, error) {
	n, err := d.int(':')
	if err != nil {
		return "", err
	}
	if err := d.expect('"'); err != nil {
		return "", err
	}
	end := d.pos + int(n)
	if n < 0 || end+1 > len(d.s) || d.s[end] != '"' {
		return "", d.errorf("bad string length %d", n)
	}
	v := d.s[d.pos:end]
	d.pos = end + 1
	return v, nil
}

func (d *decoder) value() (any, error) {
	if d.pos+1 >= len(d.s) {
		return nil, d.errorf("unexpected end")
	}
	typ := d.s[d.pos]
	if typ == 'N' {
		d.pos++
		return nil, d.expect(';')
	}
	d.pos++
	if err := d.expect(':'); err != nil {
		return nil, err
	}
	switch typ {
	case 'b':
		n, err := d.int(';')
		return n != 0, err
	case 'i':
		return d.int(';')
	case 'd':
		tok, err := d.until(';')
		if err != nil {
			return nil, err
		}
		switch tok {
		case "INF":
			tok = "+Inf"
		case "-INF":
			tok = "-Inf"
		}
		f, err := strconv.ParseFloat(tok, 64)
		if err != nil {
			return nil, d.errorf("bad float %q", tok)
		}
		return f, nil
	case 's':
		v, err := d.str()
		if err != nil {
			return nil, err
		}
		return v, d.expect(';')
	case 'a':
		n, err := d.int(':')
		if err != nil {
			return nil, err
		}
		return d.array(int(n), "")
	case 'O':
		class, err := d.str()
		if err != nil {
			return nil, err
		}
		if err := d.expect(':'); err != nil {
			return nil, err
		}
		n, err := d.int(':')
		if err != nil {
			return nil, err
		}
		return d.array(int(n), class)
	}
	return nil, d.errorf("unsupported type %q", typ)
}

// array reads `{key;value...}` with n entries.
func (d *decoder) array(n int, class string) (any, error) {
	if err := d.expect('{'); err != nil {
		return nil, err
	}
	keys := make([]string, 0, n)
	vals := make([]any, 0, n)
	sequential := class == ""
	for i := range n {
		k, err := d.value()
		if err != nil {
			return nil, err
		}
		var key string
		switch k := k.(type) {
		case int64:
			key = strconv.FormatInt(k, 10)
			if k != int64(i) {
				sequential = false
			}
		case string:
			key = k
			sequential = false
		default:
			return nil, d.errorf("bad array key type %T", k)
		}
		v, err := d.value()
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
		vals = append(vals, v)
	}
	if err := d.expect('}'); err != nil {
		return nil, err
	}
	if sequential {
		return vals, nil
	}
	m := make(map[string]any, n+1)
	for i, k := range keys {
		m[k] = vals[i]
	}
	if class != "" {
		m["__class"] = class
	}
	return m, nil
}
