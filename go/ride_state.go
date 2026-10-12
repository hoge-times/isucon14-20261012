package main

import (
	"context"
	"sync"
)

// ライドの行と最新の状態をメモリに持ち、座標と通知の SELECT を DB に撃たずに済ませる。
// 書き込みは DB に書いて成功した後にここへ反映する(ライトスルー)。再起動後は rebuild で DB から作り直す
type rideStateCache struct {
	mu sync.RWMutex
	// ride_id → rides の行
	rides map[string]Ride
	// chair_id → updated_at が最新のライドの ride_id
	chairRide map[string]string
	// user_id → created_at が最新のライドの ride_id
	userRide map[string]string
	// ride_id → created_at が最新の ride_statuses の status
	latestStatus map[string]string
}

var rideStates = newRideStateCache()

func newRideStateCache() *rideStateCache {
	return &rideStateCache{
		rides:        map[string]Ride{},
		chairRide:    map[string]string{},
		userRide:     map[string]string{},
		latestStatus: map[string]string{},
	}
}

// 状態は MATCHING から COMPLETED へ一方向にしか進まない。
// コミット後の反映が前後しても古い状態で上書きしないよう、順番が戻る更新は捨てる
var rideStatusOrder = map[string]int{
	"MATCHING":  1,
	"ENROUTE":   2,
	"PICKUP":    3,
	"CARRYING":  4,
	"ARRIVED":   5,
	"COMPLETED": 6,
}

// Evaluation はポインタなので中身も複製し、呼び出し側から map の中身を書き換えられないようにする
func cloneRide(r Ride) Ride {
	if r.Evaluation != nil {
		v := *r.Evaluation
		r.Evaluation = &v
	}
	return r
}

func (c *rideStateCache) getRide(rideID string) (Ride, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	r, ok := c.rides[rideID]
	if !ok {
		return Ride{}, false
	}
	return cloneRide(r), true
}

// SELECT * FROM rides WHERE chair_id = ? ORDER BY updated_at DESC LIMIT 1 と同じ
func (c *rideStateCache) getChairRide(chairID string) (Ride, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	r, ok := c.rides[c.chairRide[chairID]]
	if !ok {
		return Ride{}, false
	}
	return cloneRide(r), true
}

// SELECT * FROM rides WHERE user_id = ? ORDER BY created_at DESC LIMIT 1 と同じ
func (c *rideStateCache) getUserLatestRide(userID string) (Ride, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	r, ok := c.rides[c.userRide[userID]]
	if !ok {
		return Ride{}, false
	}
	return cloneRide(r), true
}

// getLatestRideStatus と同じ
func (c *rideStateCache) getLatestStatus(rideID string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s, ok := c.latestStatus[rideID]
	return s, ok
}

// DB から読み直した rides の行を入れる
func (c *rideStateCache) putRide(r Ride) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.putRideLocked(cloneRide(r))
}

func (c *rideStateCache) putRideLocked(r Ride) {
	// updated_at は進む一方なので、反映が前後したときに古い行で上書きしない
	if old, ok := c.rides[r.ID]; ok && r.UpdatedAt.Before(old.UpdatedAt) {
		return
	}
	c.rides[r.ID] = r

	if r.ChairID.Valid {
		cur, ok := c.rides[c.chairRide[r.ChairID.String]]
		if !ok || cur.ID == r.ID || !r.UpdatedAt.Before(cur.UpdatedAt) {
			c.chairRide[r.ChairID.String] = r.ID
		}
	}

	cur, ok := c.rides[c.userRide[r.UserID]]
	if !ok || cur.ID == r.ID || !r.CreatedAt.Before(cur.CreatedAt) {
		c.userRide[r.UserID] = r.ID
	}
}

// ride_statuses に INSERT した状態を入れる
func (c *rideStateCache) setStatus(rideID, status string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cur, ok := c.latestStatus[rideID]; ok && rideStatusOrder[status] < rideStatusOrder[cur] {
		return
	}
	c.latestStatus[rideID] = status
}

// rides の全行と ride_statuses の行から作り直す。状態はライドごとに created_at が最大のものを使う
func (c *rideStateCache) load(rides []Ride, statuses []RideStatus) {
	next := newRideStateCache()
	for _, r := range rides {
		next.putRideLocked(cloneRide(r))
	}
	latestAt := map[string]RideStatus{}
	for _, s := range statuses {
		if cur, ok := latestAt[s.RideID]; !ok || s.CreatedAt.After(cur.CreatedAt) {
			latestAt[s.RideID] = s
		}
	}
	for rideID, s := range latestAt {
		next.latestStatus[rideID] = s.Status
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.rides = next.rides
	c.chairRide = next.chairRide
	c.userRide = next.userRide
	c.latestStatus = next.latestStatus
}

// DB から全件を読んで作り直す。起動時と POST /api/initialize で使う
func (c *rideStateCache) rebuild(ctx context.Context, q executableQuery) error {
	rides := []Ride{}
	if err := q.SelectContext(ctx, &rides, `SELECT * FROM rides`); err != nil {
		return err
	}
	statuses := []RideStatus{}
	if err := q.SelectContext(ctx, &statuses, `SELECT ride_id, status, created_at FROM ride_statuses`); err != nil {
		return err
	}
	c.load(rides, statuses)
	return nil
}
