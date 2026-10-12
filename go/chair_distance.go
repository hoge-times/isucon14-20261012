package main

import (
	"context"
	"sync"
	"time"
)

// 椅子ごとの最新座標と総移動距離をプロセス内に持つ。
// owner/chairs が chair_locations の全履歴を毎回ウィンドウ関数で集計しないよう、
// 起動時と initialize で履歴から作り直し、以降は座標を受けるたびに前回座標との差分を足す。
// app が1プロセスであることが前提。複数台にするなら、座標の POST と owner/chairs を同じ台に寄せる。
type chairDistance struct {
	Latitude      int
	Longitude     int
	TotalDistance int
	UpdatedAt     time.Time
}

var (
	chairDistancesMu sync.RWMutex
	chairDistances   = map[string]chairDistance{}
)

// loadChairDistances は chair_locations の全履歴から、椅子ごとの最新座標と総移動距離を作り直す
func loadChairDistances(ctx context.Context) error {
	rows := []struct {
		ChairID       string    `db:"chair_id"`
		Latitude      int       `db:"latitude"`
		Longitude     int       `db:"longitude"`
		TotalDistance int       `db:"total_distance"`
		UpdatedAt     time.Time `db:"updated_at"`
	}{}
	if err := db.SelectContext(ctx, &rows, `SELECT chair_id, latitude, longitude, total_distance, created_at AS updated_at
FROM (SELECT chair_id,
             latitude,
             longitude,
             created_at,
             rn,
             SUM(IFNULL(distance, 0)) OVER (PARTITION BY chair_id) AS total_distance
      FROM (SELECT chair_id,
                   latitude,
                   longitude,
                   created_at,
                   ABS(latitude - LAG(latitude) OVER w) + ABS(longitude - LAG(longitude) OVER w) AS distance,
                   ROW_NUMBER() OVER (PARTITION BY chair_id ORDER BY created_at DESC)            AS rn
            FROM chair_locations
            WINDOW w AS (PARTITION BY chair_id ORDER BY created_at)) t1) t2
WHERE rn = 1`); err != nil {
		return err
	}

	m := make(map[string]chairDistance, len(rows))
	for _, r := range rows {
		m[r.ChairID] = chairDistance{
			Latitude:      r.Latitude,
			Longitude:     r.Longitude,
			TotalDistance: r.TotalDistance,
			UpdatedAt:     r.UpdatedAt,
		}
	}

	chairDistancesMu.Lock()
	chairDistances = m
	chairDistancesMu.Unlock()
	return nil
}

// addChairLocation は新しい座標を反映する。
// 最初の座標は距離 0 として数える(履歴の集計で LAG が NULL になり IFNULL で 0 になるのと同じ)
func addChairLocation(chairID string, latitude, longitude int, at time.Time) {
	chairDistancesMu.Lock()
	defer chairDistancesMu.Unlock()

	d, ok := chairDistances[chairID]
	if ok {
		d.TotalDistance += calculateDistance(d.Latitude, d.Longitude, latitude, longitude)
	}
	d.Latitude = latitude
	d.Longitude = longitude
	d.UpdatedAt = at
	chairDistances[chairID] = d
}

func getChairDistance(chairID string) (chairDistance, bool) {
	chairDistancesMu.RLock()
	defer chairDistancesMu.RUnlock()

	d, ok := chairDistances[chairID]
	return d, ok
}
