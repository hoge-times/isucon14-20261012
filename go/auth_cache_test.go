package main

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// GetContext のたびに回数を数え、行を dest に書く。行が無ければ sql.ErrNoRows を返す
type fakeGet struct {
	calls int
	user  *User
	chair *Chair
	// GetContext の中で走らせる処理。SELECT と chairPostActivity が競う場面を作るのに使う
	during func()
}

func (f *fakeGet) Get(dest interface{}, query string, args ...interface{}) error {
	return f.GetContext(context.Background(), dest, query, args...)
}

func (f *fakeGet) GetContext(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	f.calls++
	if f.during != nil {
		f.during()
	}
	switch d := dest.(type) {
	case *User:
		if f.user == nil {
			return sql.ErrNoRows
		}
		*d = *f.user
	case *Chair:
		if f.chair == nil {
			return sql.ErrNoRows
		}
		*d = *f.chair
	}
	return nil
}

func TestAuthCacheUserReadsDBOnlyOnce(t *testing.T) {
	c := newAuthCache()
	q := &fakeGet{user: &User{ID: "u1", AccessToken: "t1"}}

	for i := 0; i < 3; i++ {
		u, err := c.getUser(context.Background(), q, "t1")
		if err != nil {
			t.Fatal(err)
		}
		if u.ID != "u1" {
			t.Errorf("getUser: ID = %q, want u1", u.ID)
		}
	}
	if q.calls != 1 {
		t.Errorf("DB を引いた回数 = %d, want 1", q.calls)
	}
}

func TestAuthCacheDoesNotRememberInvalidToken(t *testing.T) {
	c := newAuthCache()
	q := &fakeGet{}

	for i := 0; i < 2; i++ {
		if _, err := c.getChair(context.Background(), q, "bad"); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("getChair: err = %v, want sql.ErrNoRows", err)
		}
	}
	// 覚えていないので、2回とも DB を引く
	if q.calls != 2 {
		t.Errorf("DB を引いた回数 = %d, want 2", q.calls)
	}
}

func TestAuthCacheSetChairActive(t *testing.T) {
	c := newAuthCache()
	q := &fakeGet{chair: &Chair{ID: "c1", AccessToken: "t1", IsActive: false}}

	ch, err := c.getChair(context.Background(), q, "t1")
	if err != nil {
		t.Fatal(err)
	}
	c.setChairActive(*ch, true)

	ch, err = c.getChair(context.Background(), q, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if !ch.IsActive {
		t.Error("setChairActive の後も is_active が false のまま")
	}
	if q.calls != 1 {
		t.Errorf("DB を引いた回数 = %d, want 1", q.calls)
	}
}

// SELECT が古い is_active を読んでいる間に chairPostActivity が新しい値を入れたら、新しい値を残す
func TestAuthCacheKeepsNewerActiveOverStaleRead(t *testing.T) {
	c := newAuthCache()
	q := &fakeGet{chair: &Chair{ID: "c1", AccessToken: "t1", IsActive: false}}
	q.during = func() {
		c.setChairActive(Chair{ID: "c1", AccessToken: "t1"}, true)
	}

	ch, err := c.getChair(context.Background(), q, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if !ch.IsActive {
		t.Error("返り値が古い is_active(false)になった")
	}

	q.during = nil
	ch, _ = c.getChair(context.Background(), q, "t1")
	if !ch.IsActive {
		t.Error("キャッシュが古い is_active(false)で上書きされた")
	}
}

func TestAuthCacheReset(t *testing.T) {
	c := newAuthCache()
	q := &fakeGet{user: &User{ID: "u1", AccessToken: "t1"}}

	if _, err := c.getUser(context.Background(), q, "t1"); err != nil {
		t.Fatal(err)
	}
	c.reset()
	if _, err := c.getUser(context.Background(), q, "t1"); err != nil {
		t.Fatal(err)
	}
	if q.calls != 2 {
		t.Errorf("reset の後に DB を引き直していない(回数 = %d, want 2)", q.calls)
	}
}

// 返した行を書き換えても、キャッシュの中身は変わらない
func TestAuthCacheReturnsCopy(t *testing.T) {
	c := newAuthCache()
	q := &fakeGet{chair: &Chair{ID: "c1", AccessToken: "t1", IsActive: true}}

	ch, _ := c.getChair(context.Background(), q, "t1")
	ch.IsActive = false

	ch, _ = c.getChair(context.Background(), q, "t1")
	if !ch.IsActive {
		t.Error("返り値の書き換えがキャッシュに漏れた")
	}
}
