package main

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"time"
)

type matchingChair struct {
	ID    string `db:"id"`
	Speed int    `db:"speed"`
	// 位置はメモリ(chairLocationStates)から入れる
	Latitude    int  `db:"-"`
	Longitude   int  `db:"-"`
	HasLocation bool `db:"-"`
}

// マッチングの実行と初期化を排他する。外部の matcher(isuride-matcher.service)と
// アプリ内の runMatcher が同時に走って同じ椅子を二重に割り当てないよう、マッチングは必ずこれを握って走らせる。
// 初期化の間はこれを握ったままにして、入れ直し途中の DB でマッチングしないようにする
var matchingMu sync.Mutex

const (
	// isuride-matcher.service は env.sh の ISUCON_MATCHING_INTERVAL(0.5 秒)ごとにしか叩かないので、
	// アプリ内でもっと短い間隔で回し、ライドが作られてから椅子が割り当てられるまでの待ちを縮める
	matchingInterval = 100 * time.Millisecond
	matchingTimeout  = 5 * time.Second
)

// main で起動する。matchingInterval ごとにマッチングを回す
func runMatcher() {
	ticker := time.NewTicker(matchingInterval)
	defer ticker.Stop()
	for range ticker.C {
		matchingMu.Lock()
		ctx, cancel := context.WithTimeout(context.Background(), matchingTimeout)
		if err := runMatching(ctx); err != nil {
			slog.Warn("failed to match rides, retrying next tick", "error", err)
		}
		cancel()
		matchingMu.Unlock()
	}
}

// このAPIをインスタンス内から一定間隔で叩かせることで、椅子とライドをマッチングさせる。
// アプリ内の runMatcher でも回しているので、こちらは残しているだけ
func internalGetMatching(w http.ResponseWriter, r *http.Request) {
	matchingMu.Lock()
	defer matchingMu.Unlock()
	if err := runMatching(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// 待っているライドを古い順にすべて取り、空いている椅子のうち乗車位置に最も早く着くものを割り当てる。
// matchingMu を握って呼ぶこと
func runMatching(ctx context.Context) error {
	rides := []Ride{}
	if err := db.SelectContext(ctx, &rides, `SELECT * FROM rides WHERE chair_id IS NULL ORDER BY created_at`); err != nil {
		return err
	}
	if len(rides) == 0 {
		return nil
	}

	// 空き判定は元の実装と同じく「6つの状態をすべて椅子に通知済み」。椅子は前のライドが終わるまで次を受けないので、最新の1件だけを見る。
	// 通知済みの印はメモリにしか無いので、SQL では有効な椅子をすべて取り、Go 側で空きの椅子だけに絞る
	// 最新の位置はメモリから引く。位置がまだ無い椅子も候補に残し、位置のある椅子が尽きたときだけ使う
	chairs := []matchingChair{}
	if err := db.SelectContext(ctx, &chairs, `
SELECT chairs.id, chair_models.speed
FROM chairs
  INNER JOIN chair_models ON chair_models.name = chairs.model
WHERE chairs.is_active = TRUE`); err != nil {
		return err
	}
	free := chairs[:0]
	for _, c := range chairs {
		if rideStates.isChairFree(c.ID) {
			free = append(free, c)
		}
	}
	chairs = free
	ids := make([]string, len(chairs))
	for i, c := range chairs {
		ids[i] = c.ID
	}
	positions := chairLocationStates.getMany(ids)
	for i := range chairs {
		if p, ok := positions[chairs[i].ID]; ok {
			chairs[i].Latitude, chairs[i].Longitude, chairs[i].HasLocation = p.Latitude, p.Longitude, true
		}
	}

	for _, ride := range rides {
		if len(chairs) == 0 {
			break
		}
		best := -1
		bestTime := math.Inf(1)
		for i, c := range chairs {
			t := math.MaxFloat64
			if c.HasLocation && c.Speed > 0 {
				t = float64(abs(c.Latitude-ride.PickupLatitude)+abs(c.Longitude-ride.PickupLongitude)) / float64(c.Speed)
			}
			if t < bestTime {
				best, bestTime = i, t
			}
		}
		if best < 0 {
			break
		}

		result, err := db.ExecContext(ctx, "UPDATE rides SET chair_id = ? WHERE id = ? AND chair_id IS NULL", chairs[best].ID, ride.ID)
		if err != nil {
			return err
		}
		// 割り当てられたときだけ、updated_at を DB から読み直してメモリに反映する
		if count, err := result.RowsAffected(); err != nil {
			return err
		} else if count == 1 {
			assigned := Ride{}
			if err := db.GetContext(ctx, &assigned, "SELECT * FROM rides WHERE id = ?", ride.ID); err != nil {
				return err
			}
			rideStates.putRide(assigned)
		}
		chairs[best] = chairs[len(chairs)-1]
		chairs = chairs[:len(chairs)-1]
	}

	return nil
}
