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
	c.load(rides, statuses, rebuildAllSent)

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

func pendingOf(t *testing.T, c *rideStateCache, rideID string) (app, chair []string, chairSent int) {
	t.Helper()
	c.mu.RLock()
	defer c.mu.RUnlock()
	n, ok := c.notify[rideID]
	if !ok {
		return nil, nil, 0
	}
	return append([]string{}, n.appPending...), append([]string{}, n.chairPending...), n.chairSent
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRideStateCachePendingOrder(t *testing.T) {
	c := newRideStateCache()
	c.setStatus("r1", "MATCHING")
	c.setStatus("r1", "ENROUTE")
	// 同じ状態は二重に積まない
	c.setStatus("r1", "ENROUTE")

	want := []string{"MATCHING", "ENROUTE"}
	app, chair, sent := pendingOf(t, c, "r1")
	if !equalStrings(app, want) || !equalStrings(chair, want) || sent != 0 {
		t.Fatalf("pending = %v, %v, %d; want %v, %v, 0", app, chair, sent, want, want)
	}
}

func TestRideStateCachePendingOutOfOrder(t *testing.T) {
	c := newRideStateCache()
	c.setStatus("r1", "MATCHING")
	c.setStatus("r1", "ENROUTE")
	c.setStatus("r1", "CARRYING")
	// 反映が前後して PICKUP が後から来ても、列の順番は崩れない
	c.setStatus("r1", "PICKUP")

	want := []string{"MATCHING", "ENROUTE", "PICKUP", "CARRYING"}
	app, chair, _ := pendingOf(t, c, "r1")
	if !equalStrings(app, want) || !equalStrings(chair, want) {
		t.Fatalf("pending = %v, %v; want %v", app, chair, want)
	}
	// 最新状態は戻らないが、通知の列には入る
	if s, _ := c.getLatestStatus("r1"); s != "CARRYING" {
		t.Fatalf("getLatestStatus = %q; want CARRYING", s)
	}

	// 先頭が送り済みになった後に前の状態が来ても、列の先頭に入って捨てられない
	c2 := newRideStateCache()
	c2.setStatus("r2", "ENROUTE")
	c2.markAppSent("r2", "ENROUTE")
	c2.setStatus("r2", "MATCHING")
	if s, ok := c2.peekAppPending("r2"); !ok || s != "MATCHING" {
		t.Fatalf("peekAppPending = %q, %v; want MATCHING", s, ok)
	}
}

func TestRideStateCachePeekAndMarkSent(t *testing.T) {
	c := newRideStateCache()
	if _, ok := c.peekAppPending("r1"); ok {
		t.Fatal("peekAppPending: 無いライドの未送信が見つかった")
	}
	if _, ok := c.peekChairPending("r1"); ok {
		t.Fatal("peekChairPending: 無いライドの未送信が見つかった")
	}

	c.setStatus("r1", "MATCHING")
	c.setStatus("r1", "ENROUTE")

	// のぞくだけでは取り除かない(途中で 500 を返した回は次の回に同じ状態を送る)
	for i := 0; i < 2; i++ {
		if s, ok := c.peekAppPending("r1"); !ok || s != "MATCHING" {
			t.Fatalf("peekAppPending = %q, %v; want MATCHING", s, ok)
		}
		if s, ok := c.peekChairPending("r1"); !ok || s != "MATCHING" {
			t.Fatalf("peekChairPending = %q, %v; want MATCHING", s, ok)
		}
	}

	// app と椅子の列は別々に進む
	c.markAppSent("r1", "MATCHING")
	if s, _ := c.peekAppPending("r1"); s != "ENROUTE" {
		t.Fatalf("peekAppPending = %q; want ENROUTE", s)
	}
	if s, _ := c.peekChairPending("r1"); s != "MATCHING" {
		t.Fatalf("peekChairPending = %q; want MATCHING", s)
	}

	// 先頭がのぞいた状態と違えば取り除かない(並んで来た通知の二重取り除きを防ぐ)
	c.markAppSent("r1", "MATCHING")
	if s, _ := c.peekAppPending("r1"); s != "ENROUTE" {
		t.Fatalf("peekAppPending = %q; want ENROUTE(二重に取り除いた)", s)
	}
	c.markChairSent("r1", "ENROUTE")
	if _, _, sent := pendingOf(t, c, "r1"); sent != 0 {
		t.Fatalf("chairSent = %d; want 0(先頭と違う状態で数えた)", sent)
	}

	c.markAppSent("r1", "ENROUTE")
	if _, ok := c.peekAppPending("r1"); ok {
		t.Fatal("peekAppPending: 送り切った後に未送信が残っている")
	}
}

func TestRideStateCacheChairFree(t *testing.T) {
	c := newRideStateCache()
	// ライドが無い椅子は空き
	if !c.isChairFree("c1") {
		t.Fatal("isChairFree(c1) = false; ライドが無いのに使用中")
	}

	c.putRide(Ride{ID: "r1", UserID: "u1", ChairID: chairID("c1"), CreatedAt: at(0), UpdatedAt: at(1)})
	// 状態がまだ無いライドも使用中(元の COUNT(chair_sent_at) = 0)
	if c.isChairFree("c1") {
		t.Fatal("isChairFree(c1) = true; 状態の無いライドで空き")
	}

	statuses := []string{"MATCHING", "ENROUTE", "PICKUP", "CARRYING", "ARRIVED", "COMPLETED"}
	for _, s := range statuses {
		c.setStatus("r1", s)
	}
	// 6つ積んだだけでは使用中。椅子に送り済みの数で決まる
	for i, s := range statuses {
		if c.isChairFree("c1") {
			t.Fatalf("isChairFree(c1) = true; 送り済み %d 件で空き", i)
		}
		c.markChairSent("r1", s)
	}
	if _, _, sent := pendingOf(t, c, "r1"); sent != 6 {
		t.Fatalf("chairSent = %d; want 6", sent)
	}
	if !c.isChairFree("c1") {
		t.Fatal("isChairFree(c1) = false; 6件送り済みなのに使用中")
	}

	// 次のライドが割り当たると、その送り済みの数で決まる
	c.putRide(Ride{ID: "r2", UserID: "u2", ChairID: chairID("c1"), CreatedAt: at(10), UpdatedAt: at(11)})
	if c.isChairFree("c1") {
		t.Fatal("isChairFree(c1) = true; 新しいライドが割り当たったのに空き")
	}
}

func notifyFixture() ([]Ride, []RideStatus) {
	sent := at(100)
	rides := []Ride{
		{ID: "r1", UserID: "u1", ChairID: chairID("c1"), CreatedAt: at(0), UpdatedAt: at(5)},
		{ID: "r2", UserID: "u2", ChairID: chairID("c2"), CreatedAt: at(0), UpdatedAt: at(30)},
	}
	statuses := []RideStatus{
		// r1: 走行中。app は ENROUTE から、椅子は PICKUP から未送信
		{RideID: "r1", Status: "PICKUP", CreatedAt: at(3)},
		{RideID: "r1", Status: "MATCHING", CreatedAt: at(0), AppSentAt: &sent, ChairSentAt: &sent},
		{RideID: "r1", Status: "ENROUTE", CreatedAt: at(2), ChairSentAt: &sent},
		// r2: 6つとも送り済み
		{RideID: "r2", Status: "MATCHING", CreatedAt: at(0), AppSentAt: &sent, ChairSentAt: &sent},
		{RideID: "r2", Status: "ENROUTE", CreatedAt: at(10), AppSentAt: &sent, ChairSentAt: &sent},
		{RideID: "r2", Status: "PICKUP", CreatedAt: at(15), AppSentAt: &sent, ChairSentAt: &sent},
		{RideID: "r2", Status: "CARRYING", CreatedAt: at(20), AppSentAt: &sent, ChairSentAt: &sent},
		{RideID: "r2", Status: "ARRIVED", CreatedAt: at(25), AppSentAt: &sent, ChairSentAt: &sent},
		{RideID: "r2", Status: "COMPLETED", CreatedAt: at(30), AppSentAt: &sent, ChairSentAt: &sent},
	}
	return rides, statuses
}

func TestRideStateCacheLoadUseSentAt(t *testing.T) {
	c := newRideStateCache()
	c.setStatus("old", "MATCHING")
	rides, statuses := notifyFixture()
	c.load(rides, statuses, rebuildUseSentAt)

	// 作り直す前の印は残らない
	if _, ok := c.peekAppPending("old"); ok {
		t.Error("load 前の未送信が残っている")
	}

	// *_sent_at が NULL の状態を created_at の古い順に積み、送り済みの数は chair_sent_at の数
	app, chair, sent := pendingOf(t, c, "r1")
	if !equalStrings(app, []string{"ENROUTE", "PICKUP"}) || !equalStrings(chair, []string{"PICKUP"}) || sent != 2 {
		t.Fatalf("r1 = %v, %v, %d; want [ENROUTE PICKUP], [PICKUP], 2", app, chair, sent)
	}
	app, chair, sent = pendingOf(t, c, "r2")
	if len(app) != 0 || len(chair) != 0 || sent != 6 {
		t.Fatalf("r2 = %v, %v, %d; want [], [], 6", app, chair, sent)
	}
	if c.isChairFree("c1") {
		t.Error("isChairFree(c1) = true; 走行中なのに空き")
	}
	if !c.isChairFree("c2") {
		t.Error("isChairFree(c2) = false; 6件送り済みなのに使用中")
	}
}

func TestRideStateCacheLoadAllSent(t *testing.T) {
	c := newRideStateCache()
	rides, statuses := notifyFixture()
	c.load(rides, statuses, rebuildAllSent)

	// 再起動後は DB の *_sent_at に関わらず、すべて送り済みとして作る
	app, chair, sent := pendingOf(t, c, "r1")
	if len(app) != 0 || len(chair) != 0 || sent != 3 {
		t.Fatalf("r1 = %v, %v, %d; want [], [], 3", app, chair, sent)
	}
	app, chair, sent = pendingOf(t, c, "r2")
	if len(app) != 0 || len(chair) != 0 || sent != 6 {
		t.Fatalf("r2 = %v, %v, %d; want [], [], 6", app, chair, sent)
	}
	// 通知は最新状態を返す
	if _, ok := c.peekAppPending("r1"); ok {
		t.Error("peekAppPending(r1): 送り済みのはずが未送信がある")
	}
	if s, _ := c.getLatestStatus("r1"); s != "PICKUP" {
		t.Errorf("getLatestStatus(r1) = %q; want PICKUP", s)
	}
	// 走行中の椅子は使用中のまま、終わった椅子は空き
	if c.isChairFree("c1") {
		t.Error("isChairFree(c1) = true; 走行中なのに空き")
	}
	if !c.isChairFree("c2") {
		t.Error("isChairFree(c2) = false; 終わったライドなのに使用中")
	}

	// 再起動後に届いた状態は未送信として積まれる
	c.setStatus("r1", "CARRYING")
	if s, ok := c.peekChairPending("r1"); !ok || s != "CARRYING" {
		t.Fatalf("peekChairPending(r1) = %q, %v; want CARRYING", s, ok)
	}
}
