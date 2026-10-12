package main

import (
	"math"
	"net/http"
)

type matchingChair struct {
	ID    string `db:"id"`
	Speed int    `db:"speed"`
	// 位置はメモリ(chairLocationStates)から入れる
	Latitude    int  `db:"-"`
	Longitude   int  `db:"-"`
	HasLocation bool `db:"-"`
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
	// 最新の位置はメモリから引く。位置がまだ無い椅子も候補に残し、位置のある椅子が尽きたときだけ使う
	chairs := []matchingChair{}
	if err := db.SelectContext(ctx, &chairs, `
SELECT chairs.id, chair_models.speed
FROM chairs
  INNER JOIN chair_models ON chair_models.name = chairs.model
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
