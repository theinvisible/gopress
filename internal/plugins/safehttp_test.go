package plugins

import (
	"errors"
	"strings"
	"testing"
)

func TestDisabledHTTP(t *testing.T) {
	c := DisabledHTTP()
	resp, err := c.Post("https://rest.akismet.com/1.1/comment-check", "text/plain", strings.NewReader("x"))
	if resp != nil || !errors.Is(err, ErrOutboundDisabled) {
		t.Fatalf("request went out: %v %v", resp, err)
	}
}
