package phpser

import (
	"reflect"
	"testing"
)

func TestUnmarshal(t *testing.T) {
	tests := []struct {
		in   string
		want any
	}{
		{`N;`, nil},
		{`b:1;`, true},
		{`i:-42;`, int64(-42)},
		{`d:0.5;`, 0.5},
		{`s:5:"a;b"c";`, `a;b"c`},
		{`s:4:"äö";`, "äö"}, // byte length
		{`a:0:{}`, []any{}},
		{`a:2:{i:0;i:776;i:1;i:777;}`, []any{int64(776), int64(777)}},
		{`a:2:{i:2;a:1:{s:5:"title";s:0:"";}s:12:"_multiwidget";i:1;}`, map[string]any{
			"2":            map[string]any{"title": ""},
			"_multiwidget": int64(1),
		}},
		{`O:8:"stdClass":1:{s:1:"a";b:0;}`, map[string]any{"a": false, "__class": "stdClass"}},
	}
	for _, tt := range tests {
		got, err := Unmarshal(tt.in)
		if err != nil {
			t.Errorf("Unmarshal(%q) error: %v", tt.in, err)
			continue
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Unmarshal(%q) = %#v, want %#v", tt.in, got, tt.want)
		}
	}
}

func TestUnmarshalErrors(t *testing.T) {
	for _, in := range []string{``, `s:10:"short";`, `a:1:{i:0;}`, `i:1;x`, `x:1;`} {
		if _, err := Unmarshal(in); err == nil {
			t.Errorf("Unmarshal(%q) succeeded, want error", in)
		}
	}
}

func TestMaybe(t *testing.T) {
	if got := Maybe("plain"); got != "plain" {
		t.Errorf("Maybe(plain) = %v", got)
	}
	if got := Maybe("i:3;"); got != int64(3) {
		t.Errorf("Maybe(i:3;) = %v", got)
	}
}
