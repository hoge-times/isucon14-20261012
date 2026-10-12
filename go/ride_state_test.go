package main

import (
	"database/sql"
	"testing"
	"time"
)

var baseTime = time.Date(2024, 11, 1, 0, 0, 0, 0, time.UTC)

func at(sec int) time.Time {
	return baseTime.Add(time.Duration(sec) * time.Second)
}

func chairID(id string) sql.NullString {
	return sql.NullString{String: id, Valid: true}
}

func TestRideStateCacheNotFound(t *testing.T) {
	c := newRideStateCache()
	if _, ok := c.getRide("r1"); ok {
		t.Error("getRide: 無いライドが見つかった")
	}
	if _, ok := c.getChairRide("c1"); ok {
		t.Error("getChairRide: 無い椅子のライドが見つかった")
	}
	if _, ok := c.getUserLatestRide("u1"); ok {
		t.Error("getUserLatestRide: 無いユーザーのライドが見つかった")
	}
	if _, ok := c.getLatestStatus("r1"); ok {
		t.Error("getLatestStatus: 無いライドの状態が見つかった")
	}
}

func TestRideStateCachePutAndGet(t *testing.T) {
	c := newRideStateCache()

	// 作成直後は椅子が無いので、ユーザーの最新のライドにだけ載る
	c.putRide(Ride{ID: "r1", UserID: "u1", PickupLatitude: 1, CreatedAt: at(0), UpdatedAt: at(0)})
	c.setStatus("r1", "MATCHING")
	if r, ok := c.getUserLatestRide("u1"); !ok || r.ID != "r1" {
		t.Fatalf("getUserLatestRide = %v, %v; want r1", r.ID, ok)
	}
	if _, ok := c.getChairRide("c1"); ok {
		t.Fatal("getChairRide: 割り当て前に見つかった")
	}

	// 割り当てで椅子のいまのライドに載る
	c.putRide(Ride{ID: "r1", UserID: "u1", ChairID: chairID("c1"), PickupLatitude: 1, CreatedAt: at(0), UpdatedAt: at(1)})
	if r, ok := c.getChairRide("c1"); !ok || r.ID != "r1" || r.PickupLatitude != 1 {
		t.Fatalf("getChairRide = %+v, %v; want r1", r, ok)
	}

	// 状態は進む
	for _, s := range []string{"ENROUTE", "PICKUP", "CARRYING", "ARRIVED", "COMPLETED"} {
		c.setStatus("r1", s)
		if got, ok := c.getLatestStatus("r1"); !ok || got != s {
			t.Fatalf("getLatestStatus = %q, %v; want %q", got, ok, s)
		}
	}

	// 次のライド: ユーザーの最新は created_at、椅子のいまは updated_at で決まる
	c.putRide(Ride{ID: "r2", UserID: "u1", CreatedAt: at(10), UpdatedAt: at(10)})
	if r, _ := c.getUserLatestRide("u1"); r.ID != "r2" {
		t.Fatalf("getUserLatestRide = %v; want r2", r.ID)
	}
	if r, _ := c.getChairRide("c1"); r.ID != "r1" {
		t.Fatalf("getChairRide = %v; want r1(r2 は未割り当て)", r.ID)
	}
	c.putRide(Ride{ID: "r2", UserID: "u1", ChairID: chairID("c1"), CreatedAt: at(10), UpdatedAt: at(11)})
	if r, _ := c.getChairRide("c1"); r.ID != "r2" {
		t.Fatalf("getChairRide = %v; want r2", r.ID)
	}
}

func TestRideStateCacheIgnoresStaleWrites(t *testing.T) {
	c := newRideStateCache()
	eval := 5
	c.putRide(Ride{ID: "r1", UserID: "u1", ChairID: chairID("c1"), Evaluation: &eval, CreatedAt: at(0), UpdatedAt: at(5)})

	// 反映が前後して古い行が後から来ても上書きしない
	c.putRide(Ride{ID: "r1", UserID: "u1", ChairID: chairID("c1"), CreatedAt: at(0), UpdatedAt: at(1)})
	if r, _ := c.getRide("r1"); r.Evaluation == nil || *r.Evaluation != 5 {
		t.Fatalf("getRide.Evaluation = %v; want 5", r.Evaluation)
	}

	// 椅子の古いライドが後から来ても、いまのライドは変わらない
	c.putRide(Ride{ID: "r0", UserID: "u0", ChairID: chairID("c1"), CreatedAt: at(-10), UpdatedAt: at(2)})
	if r, _ := c.getChairRide("c1"); r.ID != "r1" {
		t.Fatalf("getChairRide = %v; want r1", r.ID)
	}

	// 状態は戻らない
	c.setStatus("r1", "CARRYING")
	c.setStatus("r1", "PICKUP")
	if s, _ := c.getLatestStatus("r1"); s != "CARRYING" {
		t.Fatalf("getLatestStatus = %q; want CARRYING", s)
	}
}

func TestRideStateCacheReturnsCopy(t *testing.T) {
	c := newRideStateCache()
	eval := 3
	in := Ride{ID: "r1", UserID: "u1", ChairID: chairID("c1"), PickupLatitude: 10, Evaluation: &eval, CreatedAt: at(0), UpdatedAt: at(0)}
	c.putRide(in)

	// 渡した値を後から書き換えても map には響かない
	eval = 1
	in.PickupLatitude = 99

	for name, get := range map[string]func() (Ride, bool){
		"getRide":           func() (Ride, bool) { return c.getRide("r1") },
		"getChairRide":      func() (Ride, bool) { return c.getChairRide("c1") },
		"getUserLatestRide": func() (Ride, bool) { return c.getUserLatestRide("u1") },
	} {
		r, ok := get()
		if !ok || r.PickupLatitude != 10 || r.Evaluation == nil || *r.Evaluation != 3 {
			t.Fatalf("%s = %+v; want PickupLatitude 10, Evaluation 3", name, r)
		}
		// 返ってきた値を書き換えても map には響かない
		r.PickupLatitude = 50
		*r.Evaluation = 4
	}
	r, _ := c.getRide("r1")
	if r.PickupLatitude != 10 || *r.Evaluation != 3 {
		t.Fatalf("getRide = %+v; 返した値の書き換えが map に響いた", r)
	}
}

func TestRideStateCacheLoad(t *testing.T) {
	c := newRideStateCache()
	c.putRide(Ride{ID: "old", UserID: "u9", CreatedAt: at(0), UpdatedAt: at(0)})
	c.setStatus("old", "MATCHING")

	rides := []Ride{
		{ID: "r1", UserID: "u1", ChairID: chairID("c1"), CreatedAt: at(0), UpdatedAt: at(20)},
		{ID: "r2", UserID: "u1", ChairID: chairID("c1"), CreatedAt: at(10), UpdatedAt: at(15)},
		{ID: "r3", UserID: "u2", CreatedAt: at(5), UpdatedAt: at(5)},
	}
	statuses := []RideStatus{
		{RideID: "r1", Status: "COMPLETED", CreatedAt: at(20)},
		{RideID: "r1", Status: "MATCHING", CreatedAt: at(0)},
		{RideID: "r1", Status: "ARRIVED", CreatedAt: at(19)},
		{RideID: "r2", Status: "MATCHING", CreatedAt: at(10)},
		{RideID: "r2", Status: "ENROUTE", CreatedAt: at(15)},
		{RideID: "r3", Status: "MATCHING", CreatedAt: at(5)},
	}
	c.load(rides, statuses)

	// 作り直す前の中身は残らない
	if _, ok := c.getRide("old"); ok {
		t.Error("load 前のライドが残っている")
	}
	if _, ok := c.getUserLatestRide("u9"); ok {
		t.Error("load 前のユーザーのライドが残っている")
	}
	if _, ok := c.getLatestStatus("old"); ok {
		t.Error("load 前の状態が残っている")
	}

	// 椅子のいまは updated_at 最大、ユーザーの最新は created_at 最大
	if r, _ := c.getChairRide("c1"); r.ID != "r1" {
		t.Errorf("getChairRide(c1) = %v; want r1", r.ID)
	}
	if r, _ := c.getUserLatestRide("u1"); r.ID != "r2" {
		t.Errorf("getUserLatestRide(u1) = %v; want r2", r.ID)
	}
	if r, _ := c.getUserLatestRide("u2"); r.ID != "r3" {
		t.Errorf("getUserLatestRide(u2) = %v; want r3", r.ID)
	}

	// 状態は created_at 最大
	for rideID, want := range map[string]string{"r1": "COMPLETED", "r2": "ENROUTE", "r3": "MATCHING"} {
		if got, _ := c.getLatestStatus(rideID); got != want {
			t.Errorf("getLatestStatus(%s) = %q; want %q", rideID, got, want)
		}
	}
}
