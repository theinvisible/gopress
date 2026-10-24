package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestAuthTables(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	u := &User{Login: "Admin", Nicename: "admin", Email: "admin@example.org", Role: "administrator",
		RegisteredGMT: Time{time.Now()}}
	if err := InsertUser(ctx, d.W, u); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"admin", "ADMIN", "Admin@Example.org"} {
		if got, err := UserByLogin(ctx, d.R, name); err != nil || got.ID != u.ID {
			t.Errorf("UserByLogin(%q) = %v, %v", name, got, err)
		}
	}
	if _, err := UserByLogin(ctx, d.R, "nobody"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("unknown user: %v", err)
	}

	// TOTP steps are single-use and monotonic.
	if err := SetTOTP(ctx, d.W, u.ID, []byte("enc"), 0, 1); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		step int64
		ok   bool
	}{{100, true}, {100, false}, {99, false}, {101, true}} {
		if ok, err := UseTOTPStep(ctx, d.W, u.ID, c.step); err != nil || ok != c.ok {
			t.Errorf("UseTOTPStep(%d) = %v, %v", c.step, ok, err)
		}
	}

	// Recovery codes are single-use.
	if err := SetRecoveryCodes(ctx, d.W, u.ID, [][]byte{[]byte("a"), []byte("b")}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := UseRecoveryCode(ctx, d.W, u.ID, []byte("a"), 5); !ok {
		t.Error("first use of code failed")
	}
	if ok, _ := UseRecoveryCode(ctx, d.W, u.ID, []byte("a"), 6); ok {
		t.Error("code reused")
	}
	if n, _ := RecoveryCodesLeft(ctx, d.R, u.ID); n != 1 {
		t.Errorf("codes left = %d", n)
	}

	// Sessions: expiry by absolute and idle timeout.
	for i, s := range []Session{
		{IDHash: []byte{1}, Expires: 1000, IdleSecs: 100, LastSeen: 950},
		{IDHash: []byte{2}, Expires: 1000, IdleSecs: 100, LastSeen: 850},
		{IDHash: []byte{3}, Expires: 900, IdleSecs: 1000, LastSeen: 899},
	} {
		s.UserID, s.State, s.Created = u.ID, SessionFull, int64(i)
		if err := InsertSession(ctx, d.W, &s); err != nil {
			t.Fatal(err)
		}
	}
	if err := DeleteExpiredSessions(ctx, d.W, 960); err != nil {
		t.Fatal(err)
	}
	ss, _ := UserSessions(ctx, d.R, u.ID)
	if len(ss) != 1 || ss[0].IDHash[0] != 1 {
		t.Errorf("sessions after expiry = %+v", ss)
	}
	if n, err := IncSessionAttempts(ctx, d.W, []byte{1}); err != nil || n != 1 {
		t.Errorf("IncSessionAttempts = %d, %v", n, err)
	}
	// Ending all sessions (keep nil) and all but one.
	for _, h := range [][]byte{{7}, {8}} {
		InsertSession(ctx, d.W, &Session{IDHash: h, UserID: u.ID, State: SessionFull, Expires: 9999, LastSeen: 960, IdleSecs: 100})
	}
	DeleteUserSessions(ctx, d.W, u.ID, []byte{7})
	if ss, _ := UserSessions(ctx, d.R, u.ID); len(ss) != 1 || ss[0].IDHash[0] != 7 {
		t.Errorf("sessions after ending all but one = %+v", ss)
	}
	DeleteUserSessions(ctx, d.W, u.ID, nil)
	if ss, _ := UserSessions(ctx, d.R, u.ID); len(ss) != 0 {
		t.Errorf("sessions after ending all = %d", len(ss))
	}
}

func TestCommentStatusRestore(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	p := &Post{Type: TypePost, Status: StatusPublish, Title: "p"}
	if err := InsertPost(ctx, d.W, p); err != nil {
		t.Fatal(err)
	}
	c := &Comment{PostID: p.ID, Type: "comment", Status: CommentApproved, Content: "x"}
	if err := InsertComment(ctx, d.W, c); err != nil {
		t.Fatal(err)
	}
	status := func() string {
		got, err := GetComment(ctx, d.R, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.Status
	}
	SetCommentStatus(ctx, d.W, c.ID, CommentSpam)
	SetCommentStatus(ctx, d.W, c.ID, CommentTrash) // spam -> trash keeps "approved"
	if s := status(); s != CommentTrash {
		t.Fatalf("status = %s", s)
	}
	SetCommentStatus(ctx, d.W, c.ID, "")
	if s := status(); s != CommentApproved {
		t.Errorf("restored status = %s, want approved", s)
	}
	counts, _ := CommentStatusCounts(ctx, d.R, 0)
	if counts[CommentApproved] != 1 {
		t.Errorf("counts = %v", counts)
	}
}
