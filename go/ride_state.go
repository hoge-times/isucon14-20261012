package main

import (
	"context"
	"sort"
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
	// ride_id → 通知の送り済み / 未送信。ride_statuses の app_sent_at / chair_sent_at の代わり
	notify map[string]*rideNotifyState
}

// ライドごとの通知の状態。未送信の列は rideStatusOrder の順に並べる
type rideNotifyState struct {
	// app にまだ送っていない状態(古い順)
	appPending []string
	// 椅子にまだ送っていない状態(古い順)
	chairPending []string
	// 椅子に送り済みの状態の数。元の COUNT(chair_sent_at) に当たり、マッチングの空き判定に使う
	chairSent int
}

// rebuild で送り済みの印をどう作るか
type rebuildMode int

const (
	// POST /api/initialize の後: DB の app_sent_at / chair_sent_at をそのまま使い、初期データの通知状態を再現する
	rebuildUseSentAt rebuildMode = iota
	// 起動時: すべての状態を送り済みとして作る。*_sent_at はもう DB に書かないので、DB の値どおりだと古い状態を送り直してしまう
	rebuildAllSent
)

var rideStates = newRideStateCache()

func newRideStateCache() *rideStateCache {
	return &rideStateCache{
		rides:        map[string]Ride{},
		chairRide:    map[string]string{},
		userRide:     map[string]string{},
		latestStatus: map[string]string{},
		notify:       map[string]*rideNotifyState{},
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

// ride_statuses に INSERT した状態を入れ、ユーザーの通知(SSE)に知らせる
func (c *rideStateCache) setStatus(rideID, status string) {
	c.mu.Lock()
	// 通知が消えるとベンチが落ちるので、最新状態の更新を捨てる場合でも未送信の列には足す
	n := c.notifyLocked(rideID)
	n.appPending = insertPending(n.appPending, status)
	n.chairPending = insertPending(n.chairPending, status)

	if cur, ok := c.latestStatus[rideID]; !ok || rideStatusOrder[status] >= rideStatusOrder[cur] {
		c.latestStatus[rideID] = status
	}
	userID := c.rides[rideID].UserID
	c.mu.Unlock()

	// 未送信の列には足しているので、最新状態の更新を捨てた場合も知らせる
	appNotifier.notify(userID)
}

func (c *rideStateCache) notifyLocked(rideID string) *rideNotifyState {
	n, ok := c.notify[rideID]
	if !ok {
		n = &rideNotifyState{}
		c.notify[rideID] = n
	}
	return n
}

// 反映が前後しても順番が崩れないよう、rideStatusOrder の順を保つ位置に入れる。同じ状態が既にあれば足さない
func insertPending(pending []string, status string) []string {
	i := len(pending)
	for j, s := range pending {
		if s == status {
			return pending
		}
		if rideStatusOrder[s] > rideStatusOrder[status] {
			i = j
			break
		}
	}
	pending = append(pending, "")
	copy(pending[i+1:], pending[i:])
	pending[i] = status
	return pending
}

// app にまだ送っていない最古の状態をのぞく(取り除かない)。
// SELECT * FROM ride_statuses WHERE ride_id = ? AND app_sent_at IS NULL ORDER BY created_at ASC LIMIT 1 に当たる
func (c *rideStateCache) peekAppPending(rideID string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n, ok := c.notify[rideID]
	if !ok || len(n.appPending) == 0 {
		return "", false
	}
	return n.appPending[0], true
}

// 椅子にまだ送っていない最古の状態をのぞく(取り除かない)
func (c *rideStateCache) peekChairPending(rideID string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n, ok := c.notify[rideID]
	if !ok || len(n.chairPending) == 0 {
		return "", false
	}
	return n.chairPending[0], true
}

// のぞいた状態を app に送り済みにする。応答を書く直前に呼ぶ。
// 同じライドの通知が並んで来たときに二重に取り除かないよう、先頭がのぞいた状態と同じときだけ取り除く
func (c *rideStateCache) markAppSent(rideID, status string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n, ok := c.notify[rideID]
	if !ok || len(n.appPending) == 0 || n.appPending[0] != status {
		return
	}
	n.appPending = n.appPending[1:]
}

// のぞいた状態を椅子に送り済みにし、送り済みの数を 1 増やす
func (c *rideStateCache) markChairSent(rideID, status string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n, ok := c.notify[rideID]
	if !ok || len(n.chairPending) == 0 || n.chairPending[0] != status {
		return
	}
	n.chairPending = n.chairPending[1:]
	n.chairSent++
}

// マッチングの空き判定。いまのライドが無いか、その6つの状態をすべて椅子に送り済みなら空き。
// 元の「最新のライドの COUNT(chair_sent_at) が 6 未満なら使用中」と同じ
func (c *rideStateCache) isChairFree(chairID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	r, ok := c.rides[c.chairRide[chairID]]
	if !ok {
		return true
	}
	n, ok := c.notify[r.ID]
	return ok && n.chairSent >= 6
}

// rides の全行と ride_statuses の行から作り直す。状態はライドごとに created_at が最大のものを使う
func (c *rideStateCache) load(rides []Ride, statuses []RideStatus, mode rebuildMode) {
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

	// 未送信の列は created_at の古い順に積む
	sorted := make([]RideStatus, len(statuses))
	copy(sorted, statuses)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].CreatedAt.Before(sorted[j].CreatedAt) })
	for _, s := range sorted {
		n := next.notifyLocked(s.RideID)
		switch mode {
		case rebuildUseSentAt:
			if s.AppSentAt == nil {
				n.appPending = append(n.appPending, s.Status)
			}
			if s.ChairSentAt == nil {
				n.chairPending = append(n.chairPending, s.Status)
			} else {
				n.chairSent++
			}
		case rebuildAllSent:
			n.chairSent++
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.rides = next.rides
	c.chairRide = next.chairRide
	c.userRide = next.userRide
	c.latestStatus = next.latestStatus
	c.notify = next.notify
}

// DB から全件を読んで作り直す。起動時は rebuildAllSent、POST /api/initialize の後は rebuildUseSentAt で呼ぶ
func (c *rideStateCache) rebuild(ctx context.Context, q executableQuery, mode rebuildMode) error {
	rides := []Ride{}
	if err := q.SelectContext(ctx, &rides, `SELECT * FROM rides`); err != nil {
		return err
	}
	statuses := []RideStatus{}
	if err := q.SelectContext(ctx, &statuses, `SELECT ride_id, status, created_at, app_sent_at, chair_sent_at FROM ride_statuses`); err != nil {
		return err
	}
	c.load(rides, statuses, mode)
	return nil
}
