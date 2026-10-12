package main

import (
	"context"
	crand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
)

var db *sqlx.DB

func main() {
	mux := setup()
	srv := &http.Server{Addr: ":8080", Handler: mux}

	// systemd の停止(SIGQUIT)と OS の再起動(SIGTERM)で、座標の待ち行列を DB に書き切ってから終わる。
	// Go は SIGQUIT を既定でゴルーチンダンプして落ちるので、ここで受ける
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		slog.Info("shutting down", "signal", sig.String())
		// 新しい座標を受けないよう先に listen を閉じ、処理中のリクエストを少しだけ待つ
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := srv.Shutdown(ctx); err != nil {
			slog.Warn("failed to shutdown http server", "error", err)
		}
		cancel()
		if err := flushChairLocationsOnShutdown(5 * time.Second); err != nil {
			slog.Error("failed to flush chair locations on shutdown", "error", err)
			os.Exit(1)
		}
		os.Exit(0)
	}()

	// DB は別の台にあり、再起動直後はまだ起きていないことがある。
	// 空の map のまま応答を始めないよう、DB からライドの状態と椅子の座標を作れるまで listen しない
	for {
		// 再起動前に送った通知は DB に残っていないので、すべて送り済みとして作る
		err := rideStates.rebuild(context.Background(), db, rebuildAllSent)
		if err == nil {
			err = chairLocationStates.rebuild(context.Background(), db)
		}
		if err == nil {
			break
		}
		slog.Warn("failed to load ride states or chair locations, retrying", "error", err)
		time.Sleep(3 * time.Second)
	}

	go runChairLocationFlusher()

	slog.Info("Listening on :8080")
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("failed to listen", "error", err)
		os.Exit(1)
	}
	// Shutdown で戻ってきたときは、シグナルを受けた goroutine が書き切って os.Exit するのを待つ
	select {}
}

func setup() http.Handler {
	host := os.Getenv("ISUCON_DB_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	port := os.Getenv("ISUCON_DB_PORT")
	if port == "" {
		port = "3306"
	}
	_, err := strconv.Atoi(port)
	if err != nil {
		panic(fmt.Sprintf("failed to convert DB port number from ISUCON_DB_PORT environment variable into int: %v", err))
	}
	user := os.Getenv("ISUCON_DB_USER")
	if user == "" {
		user = "isucon"
	}
	password := os.Getenv("ISUCON_DB_PASSWORD")
	if password == "" {
		password = "isucon"
	}
	dbname := os.Getenv("ISUCON_DB_NAME")
	if dbname == "" {
		dbname = "isuride"
	}

	dbConfig := mysql.NewConfig()
	dbConfig.User = user
	dbConfig.Passwd = password
	dbConfig.Addr = net.JoinHostPort(host, port)
	dbConfig.Net = "tcp"
	dbConfig.DBName = dbname
	dbConfig.ParseTime = true
	dbConfig.InterpolateParams = true

	// Connect は DB に繋がらないとエラーになり起動時に落ちるので Open にする。繋がるまでは main() で map の作り直しをやり直して待つ
	_db, err := sqlx.Open("mysql", dbConfig.FormatDSN())
	if err != nil {
		panic(err)
	}
	// MySQL の max_connections(151)を使い切らないよう上限を決め、張り直しを避けるためアイドルも同数残す
	_db.SetMaxOpenConns(50)
	_db.SetMaxIdleConns(50)
	db = _db

	mux := chi.NewRouter()
	mux.Use(middleware.Logger)
	mux.Use(middleware.Recoverer)
	mux.HandleFunc("POST /api/initialize", postInitialize)

	// app handlers
	{
		mux.HandleFunc("POST /api/app/users", appPostUsers)

		authedMux := mux.With(appAuthMiddleware)
		authedMux.HandleFunc("POST /api/app/payment-methods", appPostPaymentMethods)
		authedMux.HandleFunc("GET /api/app/rides", appGetRides)
		authedMux.HandleFunc("POST /api/app/rides", appPostRides)
		authedMux.HandleFunc("POST /api/app/rides/estimated-fare", appPostRidesEstimatedFare)
		authedMux.HandleFunc("POST /api/app/rides/{ride_id}/evaluation", appPostRideEvaluatation)
		authedMux.HandleFunc("GET /api/app/notification", appGetNotification)
		authedMux.HandleFunc("GET /api/app/nearby-chairs", appGetNearbyChairs)
	}

	// owner handlers
	{
		mux.HandleFunc("POST /api/owner/owners", ownerPostOwners)

		authedMux := mux.With(ownerAuthMiddleware)
		authedMux.HandleFunc("GET /api/owner/sales", ownerGetSales)
		authedMux.HandleFunc("GET /api/owner/chairs", ownerGetChairs)
	}

	// chair handlers
	{
		mux.HandleFunc("POST /api/chair/chairs", chairPostChairs)

		authedMux := mux.With(chairAuthMiddleware)
		authedMux.HandleFunc("POST /api/chair/activity", chairPostActivity)
		authedMux.HandleFunc("POST /api/chair/coordinate", chairPostCoordinate)
		authedMux.HandleFunc("GET /api/chair/notification", chairGetNotification)
		authedMux.HandleFunc("POST /api/chair/rides/{ride_id}/status", chairPostRideStatus)
	}

	// internal handlers
	{
		mux.HandleFunc("GET /api/internal/matching", internalGetMatching)
	}

	return mux
}

type postInitializeRequest struct {
	PaymentServer string `json:"payment_server"`
}

type postInitializeResponse struct {
	Language string `json:"language"`
}

func postInitialize(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	req := &postInitializeRequest{}
	if err := bindJSON(r, req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	// 座標のまとめ書きを止め(書いている最中なら終わるのを待つ)、初期化の間は再開させない。
	// 待ち行列は初期化前のデータなので捨てる
	chairLocationFlushMu.Lock()
	defer chairLocationFlushMu.Unlock()
	chairLocationStates.discardPending()

	if out, err := exec.Command("../sql/init.sh").CombinedOutput(); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("failed to initialize: %s: %w", string(out), err))
		return
	}

	if _, err := db.ExecContext(ctx, "UPDATE settings SET value = ? WHERE name = 'payment_gateway_url'", req.PaymentServer); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// init.sh で入れ直したデータからライドの状態を作り直す。通知済みの印は初期データの *_sent_at をそのまま使う
	if err := rideStates.rebuild(ctx, db, rebuildUseSentAt); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// 座標のメモリも 4-chair-distances.sql で作った chair_distances から作り直す。戻るとまとめ書きが再開する
	if err := chairLocationStates.rebuild(ctx, db); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, postInitializeResponse{Language: "go"})
}

type Coordinate struct {
	Latitude  int `json:"latitude"`
	Longitude int `json:"longitude"`
}

func bindJSON(r *http.Request, v interface{}) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func writeJSON(w http.ResponseWriter, statusCode int, v interface{}) {
	w.Header().Set("Content-Type", "application/json;charset=utf-8")
	buf, err := json.Marshal(v)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(statusCode)
	w.Write(buf)
}

func writeError(w http.ResponseWriter, statusCode int, err error) {
	w.Header().Set("Content-Type", "application/json;charset=utf-8")
	w.WriteHeader(statusCode)
	buf, marshalError := json.Marshal(map[string]string{"message": err.Error()})
	if marshalError != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"marshaling error failed"}`))
		return
	}
	w.Write(buf)

	slog.Error("error response wrote", err)
}

func secureRandomStr(b int) string {
	k := make([]byte, b)
	if _, err := crand.Read(k); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", k)
}
