# ローカル環境の操作。リポジトリのルートから make -C local <target> で使う。詳細は docs/local.md。

# 全サービスで共有する資源の上限(i1 の c8i.large と同じ 2 vCPU / 4GiB)。
# CPUS は Docker Desktop の VM の CPU 番号。上限を外すなら make -C local up CPUS=0-7 MEMORY=max
CPUS ?= 0-1
MEMORY ?= 4G

compose = ISURIDE_CPUS=$(CPUS) ISURIDE_MEMORY=$(MEMORY) docker compose -f compose.yml
# コンテナの CPU アーキテクチャ(Apple Silicon なら arm64)に合わせてクロスコンパイルする
arch := $(shell docker version --format '{{.Server.Arch}}' 2>/dev/null || go env GOARCH)

upstream = upstream/isucon14
upstream_url = https://github.com/isucon/isucon14.git

# ベンチの引数。ベンチの決済サーバーは Mac 上で 12346 番を開き、アプリからは host.docker.internal で届く
bench_args = run --target https://isuride.xiv.isucon.net --addr 127.0.0.1:443 \
	--payment-url http://host.docker.internal:12346 --payment-bind-port 12346 -t 60

# 初回だけ: 公式リポジトリの bench を取得してベンチマーカーをビルドする
.PHONY: setup
setup: build/bench

$(upstream):
	git clone --depth 1 --filter=blob:none --sparse $(upstream_url) $(upstream)
	git -C $(upstream) sparse-checkout set bench

# 静的ファイルの期待値を public/ から作り直してからビルドする(AMI のビルドと同じ手順。frontendhash/main.go 参照)。
# public/ を変えたら自動で作り直す
build/bench: $(shell find ../public -type f) frontendhash/main.go | $(upstream)
	go run ./frontendhash/main.go ../public $(upstream)/bench/benchrun
	cd $(upstream)/bench && go build -o $(CURDIR)/build/bench .

# アプリを Linux 向けにビルドする(payment_mock はソースが変わったときだけ)
.PHONY: build
build: build/payment_mock
	cd ../go && CGO_ENABLED=0 GOOS=linux GOARCH=$(arch) go build -o $(CURDIR)/build/isuride .

build/payment_mock: ../payment_mock/main.go ../payment_mock/go.mod
	cd ../payment_mock && CGO_ENABLED=0 GOOS=linux GOARCH=$(arch) go build -o $(CURDIR)/build/payment_mock .

# 全体を起動する(初回は DB の初期データ投入で 1 分ほどかかる)
.PHONY: up
up: build
	mkdir -p log/nginx log/mysql
	$(compose) up -d --build --wait

# アプリだけ作り直して再起動する(コード変更後はこれ)
.PHONY: app
app: build
	$(compose) restart app

# nginx の設定を反映する(本番の make nrestart 相当。アクセスログも消す)
.PHONY: nginx
nginx:
	rm -f log/nginx/access.log
	$(compose) restart nginx

# MySQL を再起動する(設定変更の反映。スロークエリログも消す)
.PHONY: mysql-restart
mysql-restart:
	rm -f log/mysql/slow.log
	$(compose) restart db

.PHONY: down
down:
	$(compose) down

# DB のデータも消して最初からやり直す
.PHONY: clean
clean:
	$(compose) down -v

.PHONY: ps
ps:
	$(compose) ps

.PHONY: logs
logs:
	$(compose) logs -f --tail=100

.PHONY: mysql
mysql:
	$(compose) exec db mysql -uisucon -pisucon isuride

.PHONY: bench
bench: build/bench
	./build/bench $(bench_args)
