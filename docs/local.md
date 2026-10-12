# ローカル環境(Docker)

ISURIDE を Mac の Docker Desktop で動かし、手元のベンチマーカーで負荷走行するための手順。サーバー(i1〜i4)に触らずに、変更が pass するか、スコアが上がる方向かを先に確かめる用途。

ファイルは `local/` にある(`compose.yml`、`Makefile`、`Dockerfile.app`、`env.sh`、`limits.sh`、`frontendhash/`)。コマンドはすべてリポジトリのルートから `make -C local <target>` で叩く。

## 構成

全コンテナを `net` コンテナのネットワーク名前空間に入れて、i1 と同じ「1台のホスト」に見せている。そのためリポジトリの nginx / MySQL の設定と env.sh を**書き換えずにそのまま**使える(nginx → `localhost:8080`、アプリ → `127.0.0.1:3306`、マッチャー → `https://isuride.xiv.isucon.net/api/internal/matching`)。

資源も i1 に寄せていて、全サービスで **2 vCPU / 4GiB を取り合う**(後述の「資源の上限」)。

| サービス | 本番での相当 | 中身 |
|---|---|---|
| `limits` | (インスタンスタイプ) | 起動時に 1 回だけ動き、全サービスをまとめる親 cgroup に CPU とメモリの上限をかけて終了する |
| `net` | (ホストそのもの) | ネットワーク名前空間の持ち主(pause コンテナ)。`127.0.0.1:443` だけを Mac に公開する。`/etc/hosts` に `isuride.xiv.isucon.net → 127.0.0.1` と `host.docker.internal` を入れている |
| `db` | `mysql.service` | `mysql:8.0`。リポジトリの `mysql/mysql.conf.d/` を `/etc/mysql/conf.d` にマウント。データは volume `mysql-data`、ログは `local/log/mysql/` |
| `app` | `isuride-go.service` | Ubuntu 24.04 + mysql クライアント。`make build` でクロスコンパイルした `local/build/isuride` を、作業ディレクトリ `/home/isucon/webapp/go` で動かす。リポジトリ全体を `/home/isucon/webapp` に読み取り専用でマウントしているので、`/api/initialize` から `../sql/init.sh` がそのまま動く |
| `nginx` | `nginx.service` | `nginx:1.24.0`。リポジトリの `nginx/` を `/etc/nginx` に、`public/` を `/home/isucon/webapp/public` にマウント。ログは `local/log/nginx/`(LTSV) |
| `matcher` | `isuride-matcher.service` | 本番と同じ `curl` のループ。間隔は `env.sh` の `ISUCON_MATCHING_INTERVAL` |
| `payment_mock` | `isuride-payment_mock.service` | `:12345`。ブラウザで触るとき用(ベンチは自前の決済サーバーを使う) |

ベンチマーカーは Mac 上で直接動かす(`local/build/bench`)。`--addr 127.0.0.1:443` で nginx に繋ぎ、決済サーバーを Mac の 12346 番で開く。アプリからは `http://host.docker.internal:12346` で届く。

## 前提

- Docker Desktop を起動しておく
- Go(アプリとベンチのビルドに使う。`go.mod` が `go 1.23` なので 1.23 以上)
- Mac の 443 番が空いていること

## 初回セットアップ

```bash
make -C local setup   # 公式リポジトリの bench を取得してベンチマーカーをビルド
make -C local up      # ビルドして起動。初回は DB の初期データ投入があるので少し待つ
```

`setup` は公式リポジトリ [isucon/isucon14](https://github.com/isucon/isucon14) の `bench/` だけを `local/upstream/isucon14/` に取得し(`--depth 1` の sparse checkout、git 管理外)、`local/build/bench` をビルドする。ビルドの前に静的ファイルの期待値を `public/` から作り直す(後述)。

## 操作

| コマンド | やること |
|---|---|
| `make -C local up` | アプリをビルドして全体を起動(起動済みなら `compose.yml` の変更だけ反映。バイナリの入れ替えは `app` で) |
| `make -C local app` | アプリをビルドし直して再起動。**Go のコードを変えたらこれ** |
| `make -C local nginx` | nginx を再起動(設定の反映)。アクセスログも消す |
| `make -C local mysql-restart` | MySQL を再起動(設定の反映)。スロークエリログも消す |
| `make -C local bench` | ベンチを走らせる |
| `make -C local mysql` | MySQL に `isucon` ユーザーで入る |
| `make -C local ps` / `logs` | 状態とログを見る |
| `make -C local down` | 停止(DB のデータは残る) |
| `make -C local clean` | 停止して DB のデータも消す。次の `up` で初期データから作り直す |

- `make -C local app` の直後、数秒だけ 502 が返ることがある。nginx の `proxy_pass http://localhost:8080` は upstream が一度落ちると 10 秒休ませる(既定の `max_fails=1` / `fail_timeout=10s`)ため。本番の設定でも同じ挙動になる。
- `sql/` を変えたとき: ベンチは毎回 `/api/initialize` で `sql/init.sh` を流すので、ベンチ前に何かする必要はない。`up` 直後の状態から作り直したいときは `clean` → `up`。

## ベンチ

```bash
make -C local bench
```

中身は次のとおり。

```bash
./local/build/bench run --target https://isuride.xiv.isucon.net --addr 127.0.0.1:443 \
  --payment-url http://host.docker.internal:12346 --payment-bind-port 12346 -t 60
```

- 結果は stdout の `結果 pass=true スコア=N` の行で見る。初期化 → 整合性チェック → 負荷 60 秒 → 最終チェックで、全体で 1 分強かかる。
- 初期実装(main)でのスコア(2026-10-12、Apple M1 8 コア / 16GB、Docker Desktop の VM は 8 CPU / 7.7GiB):

  | 環境 | スコア |
  |---|---|
  | 本番(i4 → i1、c8i.large) | 2591 |
  | ローカル、上限 2 vCPU / 4GiB(既定) | 4513、4285(後者は WARN 1 件: CODE=26 `total_distance` の反映遅れ) |
  | ローカル、上限なし(8 CPU) | 7791 |

- ローカルのスコアは上限をかけても本番より高い(残る差は後述)。**pass するかの確認と、同じマシンでの変更前後の比較**に使い、本番の点数の予想には使わない。同じ条件でも走行ごとにぶれる(上の 2 回で約 5% の差)ので、小さな差は複数回で見る。
- 初回だけ、macOS のファイアウォールが `bench` の受信を許可するか聞いてくることがある。許可する(決済サーバーがアプリからの接続を受けるため)。

### 計測

- nginx のアクセスログ: `local/log/nginx/access.log`(本番と同じ LTSV)。alp はリポジトリの `Makefile` の `nalp` と同じ引数で使える: `alp ltsv --file local/log/nginx/access.log --sort=sum --reverse -m '...'`。
- MySQL のログ: `local/log/mysql/`。スロークエリログは、本番の `make mrestart` と同じ `set global slow_query_log = 1;`、`set global slow_query_log_file = '/var/log/mysql/slow.log';`、`set global long_query_time = 0;` を root で流すと `local/log/mysql/slow.log` に出る(`isucon` ユーザーには権限が無いので `docker compose -f local/compose.yml exec db mysql -uroot -pisucon` で入る)。`set global` は MySQL を再起動すると消える。常に有効にしたいなら `mysql/mysql.conf.d/mysqld.cnf` に書いて `mysql-restart`(本番にも効く変更になる点に注意)。
- alp と pt-query-digest はこの Mac には入っていない(2026-10-12 時点)。

## ブラウザで見る

`/etc/hosts` に次を足して `https://isuride.xiv.isucon.net/` を開く。証明書は自己署名なので警告を許可する。

```
127.0.0.1 isuride.xiv.isucon.net
```

## 資源の上限

i1(`c8i.large`)と同じく、全サービス合わせて **2 vCPU / 4GiB、スワップ無し** に制限している。

- 仕組み: 全サービスを Docker Desktop の VM 上の親 cgroup `isuride-local` に入れ(`compose.yml` の `cgroup_parent`)、`limits` サービスがその親に `cpuset.cpus=0-1` と `memory.max=4G`、`memory.swap.max=0` を書く(`local/limits.sh`)。コンテナごとの上限ではなく、**1 台のマシンを全サービスで取り合う**形になる。`limits` は VM の cgroup を書くため privileged で動く。
- ベンチは Mac 上で動くので上限の外にある(i4 が別マシンなのと同じ)。
- 変える / 外す: `make -C local up CPUS=0-3 MEMORY=6G`、上限なしは `make -C local up CPUS=0-7 MEMORY=max`。`limits` が作り直されて、動いているコンテナにもその場で効く。何も付けずに `up` すると既定(2 vCPU / 4GiB)に戻る。
- 確かめる: `docker compose -f local/compose.yml logs limits` に現在の値が出る。コンテナの中の `nproc` は 2 になる。`docker stats` の LIMIT はコンテナごとの値なので、親の上限は表示されない。
- メモリを使い切ると、本番と同じく OOM でプロセスが落ちる。MySQL の `innodb_buffer_pool_size` などを大きくするときは、ローカルでも 4GiB に収まるか確かめられる。

上限をかけても本番より点が高い。残っている差として考えられるのは次のとおり(どれが効いているかは未検証)。

- CPU の 1 コアあたりの性能が違う(Apple M1 と c8i の Xeon)。VM の CPU 0〜1 には Docker Desktop 自体の処理も乗る。
- ベンチの置き場所が違う。本番のベンチは i4(`c8i.large`、2 vCPU)で動き、VPC のネットワークを通る。ローカルは制限の無い Mac 上で動き、Docker Desktop のポート転送を通る。

## 静的ファイルの期待値を作り直す理由

ベンチは `public/` の各ファイルの MD5 を、ビルド時に埋め込んだ `bench/benchrun/frontend_hashes.json`(とパスごとの読み込みファイル一覧 `frontend_files.json`)と照合する。AMI のビルドではフロントエンドをビルドし直した直後に、公式 `frontend/vite.config.ts` の `generateHashesFile` がこの 2 つを書き換え、その後でベンチをビルドしている。そのため i4 のベンチは i1 の `public/` と合っているが、公式リポジトリにコミットされている JSON とは合わない(そのままだと「/の内容が期待したものと一致しません」で即 fail する)。

`local/frontendhash/main.go` は `generateHashesFile` を Go に移したもので、`make` がベンチをビルドする前に `public/` から 2 つの JSON を作り直す。元の TS のロジックを Node で動かした結果と、出力が完全に一致することを確認済み。`public/` を変えると、次の `make -C local bench` でベンチも自動でビルドし直される。

## 本番との差分

| 項目 | ローカル | 本番(練習環境 i1 / i4) |
|---|---|---|
| マシン | Mac(Apple M1)の Docker Desktop VM。全サービスで 2 vCPU / 4GiB(スワップ無し)を共有。ベンチは制限の無い Mac 上 | i1 は `c8i.large`(2 vCPU / 4GiB。本番の大会は `c5.large` 3 台)。ベンチは別機 i4(`c8i.large`) |
| CPU アーキテクチャ | arm64(アプリも arm64 でビルド) | amd64 |
| 台数 | 1 台相当のみ(i2 / i3 相当は無い) | 3 台 |
| MySQL | `mysql:8.0` の公式イメージ(8.0.46、Oracle Linux ベース)。設定は `mysql/mysql.conf.d/` だけを読む(`conf.d/`・`my.cnf`・`debian.cnf` は使わない)。root のパスワードは `isucon` | Ubuntu 24.04 の apt の `mysql-server`(8.0 系。正確な版はサーバーで未確認)。root は `sudo mysql` |
| nginx | `nginx:1.24.0` の公式イメージ(Debian ベース)。`modules-enabled/` は無い | Ubuntu 24.04 の nginx 1.24.0 |
| アプリのビルド | 手元の Go でクロスコンパイル(`make build` と同じやり方で、GOARCH だけ違う) | `make build` で手元の Go が作った amd64 バイナリを配布。AMI の Go は公式 provisioning では 1.23.2(サーバーで未確認) |
| プロセス管理 | docker compose(`restart: on-failure`、tini 経由) | systemd(`Restart=on-failure`) |
| env.sh | `local/env.sh`。公式 provisioning の初期値のコピー | `/home/isucon/env.sh`。**サーバーで変えたら `local/env.sh` も合わせる** |
| マッチャーの TLS | 自己署名証明書を `CURL_CA_BUNDLE` で信頼させている | 同じ `curl` コマンド(サーバーでの信頼のさせ方は未確認) |
| ベンチマーカー | 公式 main(`53f8b62`)を手元でビルドし、静的ファイルの期待値を `public/` から作り直したもの | i4 の `~/bench`(AMI に入っていたバイナリ。版はサーバーで未確認) |
| 決済サーバーへの経路 | アプリ → `host.docker.internal:12346`(Mac) | アプリ → i4 の private IP の 12346 番 |
| ファイル | リポジトリを Docker Desktop のファイル共有でマウント(静的ファイル配信の速さは本番と違う) | ローカルディスク |
| 公開ポート | Mac の `127.0.0.1:443` だけ。MySQL は `bind-address = 127.0.0.1` なので Mac から直接は繋げない(`make -C local mysql` を使う) | 443 / 22 |
