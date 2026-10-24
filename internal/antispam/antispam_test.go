package antispam

import (
	"net/url"
	"testing"
	"time"
)

func TestCheck(t *testing.T) {
	g := New([]byte("secret"))
	now := time.Unix(1_700_000_000, 0)
	tok := g.Token(7, now)
	form := func(tok, hp string) url.Values { return url.Values{TokenField: {tok}, HoneypotField: {hp}} }

	if r := g.Check(form(tok, ""), 7, "1.2.3.4", now.Add(time.Second)); r != TooFast {
		t.Errorf("fast = %q", r)
	}
	if r := g.Check(form(tok, "x"), 7, "1.2.3.4", now.Add(time.Minute)); r != Honeypot {
		t.Errorf("honeypot = %q", r)
	}
	if r := g.Check(form(tok, ""), 8, "1.2.3.4", now.Add(time.Minute)); r != BadToken {
		t.Errorf("other post = %q", r)
	}
	if r := g.Check(form(tok, ""), 7, "1.2.3.4", now.Add(72*time.Hour)); r != Expired {
		t.Errorf("old = %q", r)
	}
	for i := range RateMax {
		if r := g.Check(form(tok, ""), 7, "1.2.3.4", now.Add(time.Minute+time.Duration(i)*time.Second)); r != OK {
			t.Fatalf("submit %d = %q", i, r)
		}
	}
	if r := g.Check(form(tok, ""), 7, "1.2.3.4", now.Add(2*time.Minute)); r != RateLimited {
		t.Errorf("rate = %q", r)
	}
	if r := g.Check(form(tok, ""), 7, "5.6.7.8", now.Add(2*time.Minute)); r != OK {
		t.Errorf("other ip = %q", r)
	}
}

func TestCountLinks(t *testing.T) {
	if n := CountLinks(`see <a href="http://x">x</a> and <A title="t" HREF="y">y</A> https://z`); n != 2 {
		t.Errorf("CountLinks = %d", n)
	}
}
