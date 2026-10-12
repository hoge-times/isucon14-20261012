package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// ユーザー向けの通知を SSE で送る。
// ポーリングでは状態が変わっていなくても、毎回 認証・ライドの読み出し・運賃と椅子の成績の計算をしていた。
// 接続を張ったままにして、ライドの状態が変わったときだけ送る。
// 状態が変わったことは rideStates.setStatus(状態を INSERT したハンドラがコミットの後に呼ぶ)が appNotifier.notify で知らせる。
// 購読はプロセスのメモリ上にあるので、アプリが1プロセスで動いている前提。

// 知らせを取りこぼしたときの保険として DB を見直す間隔。仕様の「状態が変わってから3秒以内」に収まるようにする
const appNotificationFallbackInterval = 2 * time.Second

// ID(ユーザーなど)ごとに、状態が変わったことを待っている接続へ知らせる
type rideStatusNotifier struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{}
}

var appNotifier = &rideStatusNotifier{subs: map[string]map[chan struct{}]struct{}{}}

func (n *rideStatusNotifier) subscribe(id string) chan struct{} {
	// 容量1。送る側はブロックせず、受け取る側の処理中に来た知らせは1つにまとまる
	ch := make(chan struct{}, 1)
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.subs[id] == nil {
		n.subs[id] = map[chan struct{}]struct{}{}
	}
	n.subs[id][ch] = struct{}{}
	return ch
}

func (n *rideStatusNotifier) unsubscribe(id string, ch chan struct{}) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.subs[id], ch)
	if len(n.subs[id]) == 0 {
		delete(n.subs, id)
	}
}

func (n *rideStatusNotifier) notify(id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for ch := range n.subs[id] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func appGetNotificationSSE(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := ctx.Value("user").(*User)

	// DB を読む前に購読する。読んだ直後に入った状態の変化を取りこぼさないため
	ch := appNotifier.subscribe(user.ID)
	defer appNotifier.unsubscribe(user.ID, ch)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	// nginx にバッファさせない。バッファされると送った通知がクライアントに届かない
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil {
		return
	}

	ticker := time.NewTicker(appNotificationFallbackInterval)
	defer ticker.Stop()

	// 接続した直後は、未送信の状態が無くても最新の状態を送る(仕様)
	sendLatest := true
	for {
		if err := writeAppNotifications(ctx, w, rc, user, sendLatest); err != nil {
			if !errors.Is(err, context.Canceled) {
				slog.Error("failed to write app notification", "error", err)
			}
			return
		}
		sendLatest = false

		select {
		case <-ctx.Done():
			return
		case <-ch:
		case <-ticker.C:
		}
	}
}

// 最新のライドの未送信の状態を、古い順にすべて送る
func writeAppNotifications(ctx context.Context, w http.ResponseWriter, rc *http.ResponseController, user *User, sendLatest bool) error {
	ride := &Ride{}
	if err := db.GetContext(ctx, ride, `SELECT * FROM rides WHERE user_id = ? ORDER BY created_at DESC LIMIT 1`, user.ID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}

	statuses := []RideStatus{}
	if err := db.SelectContext(ctx, &statuses, `SELECT * FROM ride_statuses WHERE ride_id = ? AND app_sent_at IS NULL ORDER BY created_at ASC`, ride.ID); err != nil {
		return err
	}
	if len(statuses) == 0 && !sendLatest {
		return nil
	}

	data, err := buildAppNotificationData(ctx, user, ride)
	if err != nil {
		return err
	}

	if len(statuses) == 0 {
		status, err := getLatestRideStatus(ctx, db, ride.ID)
		if err != nil {
			return err
		}
		data.Status = status
		return writeSSEData(w, rc, data)
	}

	for _, s := range statuses {
		data.Status = s.Status
		if err := writeSSEData(w, rc, data); err != nil {
			return err
		}
		// 送れたものだけを送信済みにする。送る前に切れたら、次の接続で送り直す
		if _, err := db.ExecContext(ctx, `UPDATE ride_statuses SET app_sent_at = CURRENT_TIMESTAMP(6) WHERE id = ?`, s.ID); err != nil {
			return err
		}
	}
	return nil
}

// 通知の中身のうち、状態以外を組み立てる。読むだけなのでトランザクションは張らない
func buildAppNotificationData(ctx context.Context, user *User, ride *Ride) (*appGetNotificationResponseData, error) {
	fare, err := calculateDiscountedFare(ctx, db, user.ID, ride, ride.PickupLatitude, ride.PickupLongitude, ride.DestinationLatitude, ride.DestinationLongitude)
	if err != nil {
		return nil, err
	}

	data := &appGetNotificationResponseData{
		RideID: ride.ID,
		PickupCoordinate: Coordinate{
			Latitude:  ride.PickupLatitude,
			Longitude: ride.PickupLongitude,
		},
		DestinationCoordinate: Coordinate{
			Latitude:  ride.DestinationLatitude,
			Longitude: ride.DestinationLongitude,
		},
		Fare:      fare,
		CreatedAt: ride.CreatedAt.UnixMilli(),
		UpdateAt:  ride.UpdatedAt.UnixMilli(),
	}

	if ride.ChairID.Valid {
		chair := &Chair{}
		if err := db.GetContext(ctx, chair, `SELECT * FROM chairs WHERE id = ?`, ride.ChairID); err != nil {
			return nil, err
		}

		stats, err := getChairStats(ctx, db, chair.ID)
		if err != nil {
			return nil, err
		}

		data.Chair = &appGetNotificationResponseChair{
			ID:    chair.ID,
			Name:  chair.Name,
			Model: chair.Model,
			Stats: stats,
		}
	}

	return data, nil
}

// 1件を1行の data: で送り、空行で区切る
func writeSSEData(w http.ResponseWriter, rc *http.ResponseController, v any) error {
	buf, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", buf); err != nil {
		return err
	}
	return rc.Flush()
}
