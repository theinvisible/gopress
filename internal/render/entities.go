package render

import (
	"regexp"
	"strconv"
)

var reNumericEntity = regexp.MustCompile(`&#(x[0-9a-fA-F]+|[0-9]+);`)

// DecodeNumericEntities replaces numeric character references (as produced
// by wptexturize) with the characters, leaving named entities alone.
func DecodeNumericEntities(s string) string {
	return reNumericEntity.ReplaceAllStringFunc(s, func(m string) string {
		body := m[2 : len(m)-1]
		var n int64
		var err error
		if body[0] == 'x' || body[0] == 'X' {
			n, err = strconv.ParseInt(body[1:], 16, 32)
		} else {
			n, err = strconv.ParseInt(body, 10, 32)
		}
		if err != nil || n <= 0 || n > 0x10FFFF || n >= 0xD800 && n <= 0xDFFF {
			return m
		}
		if n == '&' || n == '<' || n == '>' || n == '"' || n == '\'' {
			return m
		}
		return string(rune(n))
	})
}
