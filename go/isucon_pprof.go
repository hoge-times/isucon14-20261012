package main

// isucon-pprof-setup が置いたファイル。CPU プロファイルを localhost:6060 で取れるようにする。
// アプリのルーター(chi / echo / gin など)とは別の http.Server で動くので、ルーティングには影響しない。
// 外から叩かれないよう listen は localhost だけに絞っている。
// import に別名を付けているのは、アプリ側の識別子(log など)と衝突させないため。

import (
	pprofhttp "net/http"
	_ "net/http/pprof"
)

func init() {
	go func() {
		println("pprof:", pprofhttp.ListenAndServe("localhost:6060", nil).Error())
	}()
}
