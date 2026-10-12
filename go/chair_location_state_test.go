package main

import (
	"strings"
	"testing"
)

func TestChairLocationStateDistance(t *testing.T) {
	s := newChairLocationState()

	if _, ok := s.get("c1"); ok {
		t.Fatal("座標を送っていない椅子が見つかった")
	}

	// 初めての座標は距離 0
	s.record("l1", "c1", 10, 20, at(0))
	p, ok := s.get("c1")
	if !ok {
		t.Fatal("座標が記録されていない")
	}
	if p.TotalDistance != 0 || p.Latitude != 10 || p.Longitude != 20 || !p.UpdatedAt.Equal(at(0)) {
		t.Errorf("初回: got %+v", p)
	}

	// 前の座標とのマンハッタン距離を足していく
	s.record("l2", "c1", 7, 25, at(1))  // 3 + 5
	s.record("l3", "c1", 7, 25, at(2))  // 0
	s.record("l4", "c1", -3, 24, at(3)) // 10 + 1
	p, _ = s.get("c1")
	if p.TotalDistance != 19 || p.Latitude != -3 || p.Longitude != 24 || !p.UpdatedAt.Equal(at(3)) {
		t.Errorf("積算: got %+v", p)
	}

	// 椅子ごとに別に数える
	s.record("l5", "c2", 100, 100, at(4))
	p, _ = s.get("c2")
	if p.TotalDistance != 0 {
		t.Errorf("別の椅子の初回: got %+v", p)
	}

	got := s.getMany([]string{"c1", "c2", "c3"})
	if len(got) != 2 || got["c1"].TotalDistance != 19 || got["c2"].Latitude != 100 {
		t.Errorf("getMany: got %+v", got)
	}
}

func TestChairLocationStateTakeAndRestore(t *testing.T) {
	s := newChairLocationState()
	if !s.takePending().empty() {
		t.Fatal("何も積んでいないのに取り出せた")
	}

	s.record("l1", "c1", 0, 0, at(0))
	s.record("l2", "c1", 1, 1, at(1))
	s.record("l3", "c2", 5, 5, at(2))

	b := s.takePending()
	if len(b.locations) != 3 || b.locations[0].ID != "l1" || b.locations[1].ID != "l2" || b.locations[2].ID != "l3" {
		t.Fatalf("locations: got %+v", b.locations)
	}
	if len(b.distances) != 2 {
		t.Fatalf("distances: got %+v", b.distances)
	}
	for _, d := range b.distances {
		switch d.ChairID {
		case "c1":
			if d.TotalDistance != 2 || d.Latitude != 1 || !d.TotalDistanceUpdatedAt.Equal(at(1)) {
				t.Errorf("c1: got %+v", d)
			}
		case "c2":
			if d.TotalDistance != 0 || d.Latitude != 5 {
				t.Errorf("c2: got %+v", d)
			}
		default:
			t.Errorf("想定外の椅子: %+v", d)
		}
	}
	if s.hasPending() {
		t.Error("取り出した後も待ち行列が残っている")
	}

	// 書き込みに失敗している間に新しい座標が来て、その後に戻す
	s.record("l4", "c1", 4, 1, at(3))
	s.restorePending(b)

	b2 := s.takePending()
	ids := make([]string, len(b2.locations))
	for i, l := range b2.locations {
		ids[i] = l.ID
	}
	if strings.Join(ids, ",") != "l1,l2,l3,l4" {
		t.Errorf("戻した後の順番: got %v", ids)
	}
	// chair_distances は戻した時点ではなく、取り出した時点の最新のメモリの値になる
	if len(b2.distances) != 2 {
		t.Fatalf("distances: got %+v", b2.distances)
	}
	for _, d := range b2.distances {
		if d.ChairID == "c1" && (d.TotalDistance != 5 || d.Latitude != 4 || !d.TotalDistanceUpdatedAt.Equal(at(3))) {
			t.Errorf("c1: got %+v", d)
		}
	}

	// 捨てると空になるが、メモリの座標は残る
	s.record("l5", "c2", 6, 5, at(4))
	s.discardPending()
	if s.hasPending() {
		t.Error("捨てた後も待ち行列が残っている")
	}
	if p, _ := s.get("c2"); p.TotalDistance != 1 {
		t.Errorf("捨てた後の座標: got %+v", p)
	}
}

func TestChairLocationStateLoad(t *testing.T) {
	s := newChairLocationState()
	s.record("l1", "old", 1, 1, at(0))

	s.load([]chairDistance{
		{ChairID: "c1", Latitude: 10, Longitude: 20, TotalDistance: 300, TotalDistanceUpdatedAt: at(5)},
	})

	if _, ok := s.get("old"); ok {
		t.Error("作り直す前の椅子が残っている")
	}
	if s.hasPending() {
		t.Error("作り直した後も待ち行列が残っている")
	}
	p, ok := s.get("c1")
	if !ok || p.TotalDistance != 300 || p.Latitude != 10 || p.Longitude != 20 || !p.UpdatedAt.Equal(at(5)) {
		t.Errorf("作り直した値: got %+v", p)
	}

	// 作り直した値に続けて積算する
	s.record("l2", "c1", 12, 17, at(6))
	p, _ = s.get("c1")
	if p.TotalDistance != 305 {
		t.Errorf("作り直した後の積算: got %+v", p)
	}
}

func TestPlaceholders(t *testing.T) {
	if got := placeholders("(?, ?)", 3); got != "(?, ?), (?, ?), (?, ?)" {
		t.Errorf("got %q", got)
	}
}
