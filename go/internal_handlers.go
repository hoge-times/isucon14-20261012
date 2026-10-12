package main

import (
	"database/sql"
	"math"
	"net/http"
)

type matchingChair struct {
	ID        string        `db:"id"`
	Speed     int           `db:"speed"`
	Latitude  sql.NullInt64 `db:"latitude"`
	Longitude sql.NullInt64 `db:"longitude"`
}

// このAPIをインスタンス内から一定間隔で叩かせることで、椅子とライドをマッチングさせる
// 待っているライドを古い順にすべて取り、空いている椅子のうち乗車位置に最も早く着くものを割り当てる
func internalGetMatching(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	rides := []Ride{}
	if err := db.SelectContext(ctx, &rides, `SELECT * FROM rides WHERE chair_id IS NULL ORDER BY created_at`); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if len(rides) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// 空き判定は元の実装と同じく「6つの状態をすべて椅子に通知済み」。椅子は前のライドが終わるまで次を受けないので、最新の1件だけを見る。
	// 通知済みの印はメモリにしか無いので、SQL では有効な椅子をすべて取り、Go 側で空きの椅子だけに絞る
	// 最新の位置は ORDER BY に chair_id を含めると、インデックスの逆順読みで1行だけ引ける(含めないと filesort で全履歴を読む)
	// 位置がまだ無い椅子も候補に残し、位置のある椅子が尽きたときだけ使う
	chairs := []matchingChair{}
	if err := db.SelectContext(ctx, &chairs, `
SELECT chairs.id, chair_models.speed, chair_locations.latitude, chair_locations.longitude
FROM chairs
  INNER JOIN chair_models ON chair_models.name = chairs.model
  LEFT JOIN chair_locations ON chair_locations.id = (
    SELECT id FROM chair_locations WHERE chair_id = chairs.id ORDER BY chair_id DESC, created_at DESC LIMIT 1
  )
WHERE chairs.is_active = TRUE`); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	free := chairs[:0]
	for _, c := range chairs {
		if rideStates.isChairFree(c.ID) {
			free = append(free, c)
		}
	}
	chairs = free

	for _, ride := range rides {
		if len(chairs) == 0 {
			break
		}
		best := -1
		bestTime := math.Inf(1)
		for i, c := range chairs {
			t := math.MaxFloat64
			if c.Latitude.Valid && c.Longitude.Valid && c.Speed > 0 {
				t = float64(abs(int(c.Latitude.Int64)-ride.PickupLatitude)+abs(int(c.Longitude.Int64)-ride.PickupLongitude)) / float64(c.Speed)
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
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		// 割り当てられたときだけ、updated_at を DB から読み直してメモリに反映する
		if count, err := result.RowsAffected(); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		} else if count == 1 {
			assigned := Ride{}
			if err := db.GetContext(ctx, &assigned, "SELECT * FROM rides WHERE id = ?", ride.ID); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			rideStates.putRide(assigned)
		}
		chairs[best] = chairs[len(chairs)-1]
		chairs = chairs[:len(chairs)-1]
	}

	w.WriteHeader(http.StatusNoContent)
}
