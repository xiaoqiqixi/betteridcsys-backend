package billing

import (
	"fmt"
	"testing"
	"time"
)

func TestMySQLSessionCreationPurgesBoundedExpiredRows(t *testing.T) {
	f := newCancellationSQLFixture(t)
	for n := 0; n < 300; n++ {
		migrationExec(t, f.ctx, f.m.DB, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES(?,?,?)", fmt.Sprintf("%064d", n), f.owner.ID, time.Now().UTC().Add(-time.Hour))
	}
	live := Session{Hash: digest(ID()), UserID: f.owner.ID, Expires: time.Now().UTC().Add(time.Hour)}
	if err := f.m.PutSession(f.ctx, live); err != nil {
		t.Fatal(err)
	}
	var expired int
	if err := f.m.DB.QueryRow("SELECT COUNT(*) FROM sessions WHERE expires_at<=UTC_TIMESTAMP(6)").Scan(&expired); err != nil {
		t.Fatal(err)
	}
	if expired != 44 {
		t.Fatal("expected one bounded batch", expired)
	}
	if _, err := f.m.Session(f.ctx, live.Hash); err != nil {
		t.Fatal("live session lost", err)
	}
	live.Hash = digest(ID())
	if err := f.m.PutSession(f.ctx, live); err != nil {
		t.Fatal(err)
	}
	if err := f.m.DB.QueryRow("SELECT COUNT(*) FROM sessions WHERE expires_at<=UTC_TIMESTAMP(6)").Scan(&expired); err != nil || expired != 0 {
		t.Fatal(expired, err)
	}
}
