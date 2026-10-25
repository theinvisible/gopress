package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestEnvBool(t *testing.T) {
	for v, want := range map[string]bool{"": false, "true": true, "1": true, "yes": true, "ON": true, "false": false, "0": false, "no": false, "off": false} {
		t.Setenv("GP_TEST_BOOL", v)
		if got, err := envBool("GP_TEST_BOOL"); err != nil || got != want {
			t.Errorf("%q: %v %v", v, got, err)
		}
	}
	for _, v := range []string{"y", "enabled", "tru"} {
		t.Setenv("GP_TEST_BOOL", v)
		if _, err := envBool("GP_TEST_BOOL"); err == nil {
			t.Errorf("%q accepted", v)
		}
	}
}

func TestServeRefusesBadSwitches(t *testing.T) {
	t.Setenv("GOPRESS_DISABLE_OUTBOUND", "enabled")
	var out, errb bytes.Buffer
	if code := run([]string{"serve", "--data", t.TempDir(), "--addr", "127.0.0.1:0"}, &out, &errb); code != 2 ||
		!strings.Contains(errb.String(), "GOPRESS_DISABLE_OUTBOUND") {
		t.Errorf("serve started with a bad switch: %d %q", code, errb.String())
	}
}

func TestImportDSNNotPrinted(t *testing.T) {
	t.Setenv("GOPRESS_IMPORT_DSN", "user:S3cretPW@tcp(db:3306)/wp")
	var out, errb bytes.Buffer
	run([]string{"import-wp", "--typo"}, &out, &errb)
	run([]string{"import-wp", "-h"}, &out, &errb)
	if strings.Contains(out.String()+errb.String(), "S3cretPW") {
		t.Error("the DSN password was printed")
	}
}
