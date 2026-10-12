package main

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
)

// 椅子の座標をメモリに持ち、POST /api/chair/coordinate のたびに DB へ書かずに済ませる。
// nearby-chairs / マッチング / owner/chairs はここから読み、DB へは裏で 500ms ごとにまとめて書く
type chairLocationState struct {
	mu sync.Mutex
	// chair_id → 最新の座標と総移動距離。chair_distances の行と同じ意味
	positions map[string]chairPosition
	// DB に書く待ち行列: chair_locations に入れる行(古い順)
	pendingLocations []ChairLocation
	// DB に書く待ち行列: chair_distances を書き直す必要がある椅子
	dirtyChairs map[string]struct{}
}

type chairPosition struct {
	Latitude      int
	Longitude     int
	TotalDistance int
	// 最新の座標の登録日時(chair_distances.total_distance_updated_at)
	UpdatedAt time.Time
}

// chair_distances の行
type chairDistance struct {
	ChairID                string    `db:"chair_id"`
	Latitude               int       `db:"latitude"`
	Longitude              int       `db:"longitude"`
	TotalDistance          int       `db:"total_distance"`
	TotalDistanceUpdatedAt time.Time `db:"total_distance_updated_at"`
}

// takePending で取り出した 1 回分の書き込み
type chairLocationBatch struct {
	locations []ChairLocation
	// 取り出した時点のメモリの値。差分ではなくこの値をそのまま書く
	distances []chairDistance
}

func (b chairLocationBatch) empty() bool {
	return len(b.locations) == 0 && len(b.distances) == 0
}

var chairLocationStates = newChairLocationState()

func newChairLocationState() *chairLocationState {
	return &chairLocationState{
		positions:   map[string]chairPosition{},
		dirtyChairs: map[string]struct{}{},
	}
}

// 座標を受けたときに呼ぶ。前の座標とのマンハッタン距離を総移動距離に足し、待ち行列に積む。初めての座標は距離 0
func (s *chairLocationState) record(id, chairID string, latitude, longitude int, createdAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.positions[chairID]
	if ok {
		p.TotalDistance += abs(latitude-p.Latitude) + abs(longitude-p.Longitude)
	}
	p.Latitude = latitude
	p.Longitude = longitude
	p.UpdatedAt = createdAt
	s.positions[chairID] = p

	s.pendingLocations = append(s.pendingLocations, ChairLocation{
		ID:        id,
		ChairID:   chairID,
		Latitude:  latitude,
		Longitude: longitude,
		CreatedAt: createdAt,
	})
	s.dirtyChairs[chairID] = struct{}{}
}

func (s *chairLocationState) get(chairID string) (chairPosition, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.positions[chairID]
	return p, ok
}

// 複数の椅子の座標を 1 回のロックで引く。座標が無い椅子は結果に入らない
func (s *chairLocationState) getMany(chairIDs []string) map[string]chairPosition {
	s.mu.Lock()
	defer s.mu.Unlock()
	res := make(map[string]chairPosition, len(chairIDs))
	for _, id := range chairIDs {
		if p, ok := s.positions[id]; ok {
			res[id] = p
		}
	}
	return res
}

// 待ち行列を空のものと入れ替えて取り出す。chair_distances はこの時点のメモリの値で書く
func (s *chairLocationState) takePending() chairLocationBatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := chairLocationBatch{locations: s.pendingLocations}
	for id := range s.dirtyChairs {
		p := s.positions[id]
		b.distances = append(b.distances, chairDistance{
			ChairID:                id,
			Latitude:               p.Latitude,
			Longitude:              p.Longitude,
			TotalDistance:          p.TotalDistance,
			TotalDistanceUpdatedAt: p.UpdatedAt,
		})
	}
	s.pendingLocations = nil
	s.dirtyChairs = map[string]struct{}{}
	return b
}

// 書き込みに失敗した分を待ち行列に戻す。取り出した後に積まれた行より前に戻し、順番を崩さない。
// chair_distances は椅子の印だけ戻し、次の回に最新のメモリの値で書く
func (s *chairLocationState) restorePending(b chairLocationBatch) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(b.locations) > 0 {
		s.pendingLocations = append(b.locations, s.pendingLocations...)
	}
	for _, d := range b.distances {
		s.dirtyChairs[d.ChairID] = struct{}{}
	}
}

func (s *chairLocationState) hasPending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pendingLocations) > 0 || len(s.dirtyChairs) > 0
}

func (s *chairLocationState) discardPending() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingLocations = nil
	s.dirtyChairs = map[string]struct{}{}
}

// chair_distances の全行から作り直す。待ち行列は捨てる
func (s *chairLocationState) load(rows []chairDistance) {
	positions := make(map[string]chairPosition, len(rows))
	for _, r := range rows {
		positions[r.ChairID] = chairPosition{
			Latitude:      r.Latitude,
			Longitude:     r.Longitude,
			TotalDistance: r.TotalDistance,
			UpdatedAt:     r.TotalDistanceUpdatedAt,
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.positions = positions
	s.pendingLocations = nil
	s.dirtyChairs = map[string]struct{}{}
}

func (s *chairLocationState) rebuild(ctx context.Context, q executableQuery) error {
	rows := []chairDistance{}
	if err := q.SelectContext(ctx, &rows, `SELECT chair_id, latitude, longitude, total_distance, total_distance_updated_at FROM chair_distances`); err != nil {
		return err
	}
	s.load(rows)
	return nil
}

// まとめ書きと、初期化・停止時の書き切りを排他する。
// 初期化の間はこれを握ったままにして、まとめ書きを止める
var chairLocationFlushMu sync.Mutex

const (
	chairLocationFlushInterval = 500 * time.Millisecond
	chairLocationFlushTimeout  = 5 * time.Second
	// 1 文あたりの行数。chair_locations は 5 列なのでプレースホルダは 5000 個(上限 65535)
	chairLocationFlushChunk = 1000
)

// 待ち行列を取り出して 1 つのトランザクションで書く。失敗したら取り出した分を待ち行列に戻す
func flushChairLocations(ctx context.Context, d *sqlx.DB, s *chairLocationState) error {
	b := s.takePending()
	if b.empty() {
		return nil
	}
	if err := writeChairLocationBatch(ctx, d, b); err != nil {
		s.restorePending(b)
		return err
	}
	return nil
}

func writeChairLocationBatch(ctx context.Context, d *sqlx.DB, b chairLocationBatch) error {
	tx, err := d.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// COMMIT の応答だけが失われて同じ行を書き直したときに主キーの重複で詰まらないよう、重複は無視する
	for start := 0; start < len(b.locations); start += chairLocationFlushChunk {
		chunk := b.locations[start:min(start+chairLocationFlushChunk, len(b.locations))]
		args := make([]any, 0, len(chunk)*5)
		for _, l := range chunk {
			args = append(args, l.ID, l.ChairID, l.Latitude, l.Longitude, l.CreatedAt)
		}
		query := `INSERT INTO chair_locations (id, chair_id, latitude, longitude, created_at) VALUES ` +
			placeholders("(?, ?, ?, ?, ?)", len(chunk)) +
			` ON DUPLICATE KEY UPDATE id = id`
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return err
		}
	}

	// 差分を足さずメモリの値をそのまま書くので、同じ書き込みが 2 回入っても結果は変わらない
	for start := 0; start < len(b.distances); start += chairLocationFlushChunk {
		chunk := b.distances[start:min(start+chairLocationFlushChunk, len(b.distances))]
		args := make([]any, 0, len(chunk)*5)
		for _, r := range chunk {
			args = append(args, r.ChairID, r.Latitude, r.Longitude, r.TotalDistance, r.TotalDistanceUpdatedAt)
		}
		query := `INSERT INTO chair_distances (chair_id, latitude, longitude, total_distance, total_distance_updated_at) VALUES ` +
			placeholders("(?, ?, ?, ?, ?)", len(chunk)) +
			` AS new ON DUPLICATE KEY UPDATE
  latitude = new.latitude,
  longitude = new.longitude,
  total_distance = new.total_distance,
  total_distance_updated_at = new.total_distance_updated_at`
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func placeholders(row string, n int) string {
	var sb strings.Builder
	sb.Grow((len(row) + 2) * n)
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(row)
	}
	return sb.String()
}

// main で起動する。500ms ごとに待ち行列を DB に書く
func runChairLocationFlusher() {
	ticker := time.NewTicker(chairLocationFlushInterval)
	defer ticker.Stop()
	for range ticker.C {
		chairLocationFlushMu.Lock()
		ctx, cancel := context.WithTimeout(context.Background(), chairLocationFlushTimeout)
		if err := flushChairLocations(ctx, db, chairLocationStates); err != nil {
			slog.Warn("failed to flush chair locations, retrying next tick", "error", err)
		}
		cancel()
		chairLocationFlushMu.Unlock()
	}
}

// 停止時に呼ぶ。動いているまとめ書きが終わるのを待ち、待ち行列が空になるまで書く
func flushChairLocationsOnShutdown(timeout time.Duration) error {
	chairLocationFlushMu.Lock()
	defer chairLocationFlushMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for chairLocationStates.hasPending() {
		if err := flushChairLocations(ctx, db, chairLocationStates); err != nil {
			if ctx.Err() != nil {
				return err
			}
			slog.Warn("failed to flush chair locations on shutdown, retrying", "error", err)
			time.Sleep(200 * time.Millisecond)
		}
	}
	return nil
}
