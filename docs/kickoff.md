# 大会プロファイル: isucon14(練習)

ISUCON14(ISURIDE)の過去問を、matsuu/aws-isucon の AMI で立てた練習環境で解く。2026-10-12 キックオフ。

- 種別: **練習**(ISUCON14 は本選のみの大会。AMI `ami-0fcf9e8e8675a9ee4` / `isucon14-20260818100152`)
- メンバー: `iwashi623` / `meihei3` / `sucalul`
- リポジトリ: `hoge-times/isucon14-20261012`(public)
- 資料: 同じ `docs/` にある `manual.md`(当日マニュアル)、`ISURIDE.md`(アプリケーションマニュアル)、`regulation.md`、`isucon14-README.md`、`matsuu-aws-isucon14-README.md`

## 対象ホスト

- **`HOSTS="i1 i2 i3"`**。先頭ホストは **`i1`**(リポジトリの init / push、`make bench` の基点)
- **ベンチマーカー専用: `i4`**(`HOSTS` に含めない)。アプリと同じ AMI なので isuride のサービスも動いているが、使わない。鍵の配布とツールの導入は `i4` にも行う

| alias | public IP | private IP | instance |
|---|---|---|---|
| i1 | 3.112.206.125 | 172.31.0.155 | i-09a2ab6e2c669f026 |
| i2 | 13.115.226.135 | 172.31.0.207 | i-0e42a954c74f5664b |
| i3 | 52.195.184.187 | 172.31.0.116 | i-0399d0941f361e543 |
| i4 | 54.249.209.105 | 172.31.0.144 | i-0cb5e85d975613256 |

インスタンスタイプはすべて `c8i.large`(本番は `c5.large` x3、ベンチは Fargate 8vCPU)。

## webapp

- ディレクトリ: `/home/isucon/webapp`(言語別に `go/` `nodejs/` `perl/` `php/` `python/` `ruby/` `rust/`。ほかに `sql/` `public/` `payment_mock/` `openapi.yaml`)
- Go のバイナリ: `/home/isucon/webapp/go/isuride`(`WorkingDirectory=/home/isucon/webapp/go`、`EnvironmentFile=/home/isucon/env.sh`)
- nginx: `/etc/nginx/sites-enabled/isuride.conf`。ホスト名は `isuride.xiv.isucon.net`(練習環境の証明書は自己署名)

## サービス名

| 用途 | systemd ユニット |
|---|---|
| アプリ(Go、初期状態で稼働) | `isuride-go.service` |
| 他言語 | `isuride-{node,perl,php,python,ruby,rust}.service` |
| マッチング(`GET /api/internal/matching` を 500ms ごとにポーリング) | `isuride-matcher.service` |
| 決済モック(:12345) | `isuride-payment_mock.service` |

## 言語切り替え手順(manual.md より)

```bash
sudo systemctl disable --now isuride-go.service
sudo systemctl enable --now isuride-{言語名}.service
# PHP のみ: sudo ln -s /etc/nginx/sites-available/isuride-php.conf /etc/nginx/sites-enabled/ && sudo systemctl restart nginx.service
```

## DB 接続情報(`/home/isucon/env.sh`、2026-10-12 に i1 で確認)

- `ISUCON_DB_HOST=127.0.0.1` / `ISUCON_DB_PORT=3306` / `ISUCON_DB_USER=isucon` / `ISUCON_DB_PASSWORD=isucon` / `ISUCON_DB_NAME=isuride`
- ユーザーは `webapp/sql/0-init.sql` で `'isucon'@'%'` として作成される(別台から接続可能)
- 管理者接続: `sudo mysql isuride`。初期化スクリプト: `~/webapp/sql/init.sh`
- マッチング間隔: `env.sh` の `ISUCON_MATCHING_INTERVAL=0.5`(変更後に `sudo systemctl restart isuride-matcher.service`)

## 初期化エンドポイント

- `POST /api/initialize`(タイムアウト 30 秒)。ベンチが `payment_server` を渡す
- レスポンス JSON の `language` フィールドに実装言語を出す必要がある
- 処理やデータ構造を変えたら、初期化で行っている内容を漏れなく残すこと

## ベンチ実行方法(練習)

ベンチは **`i4` から先頭ホスト `i1` に向けて**実行する。payment mock はベンチマーカー自身が `i4` の 12346 番で立てるので、`--payment-url` は `i4` の private IP にする。

```bash
ssh i4 'cd ~ && ./bench run . run --addr 172.31.0.155:443 --target https://isuride.xiv.isucon.net --payment-url http://172.31.0.144:12346 --payment-bind-port 12346'
```

- pass / スコアは stdout の `結果 pass=true スコア=N` の行から読む(`/tmp/result.json` は出ない)。この行が出ずに終わったら失敗扱い
- 参考: 同一ホストで回す場合(`contests.md` の行)は `ssh i1 'cd ~ && ./bench run . run --addr 127.0.0.1:443 --target https://isuride.xiv.isucon.net --payment-url http://127.0.0.1:12346 --payment-bind-port 12346'`
- 本番ではポータルから1台を選んで走行する。追試も最終スコアが記録された台に対して行う

## 特記事項

- スコア = 「マッチ位置→乗車位置の移動距離合計 × 0.1」+「乗車位置→目的地の移動距離合計」+「ライド完了数 × 5」
- 負荷走行: 初期化 → 整合性チェック → 負荷 60 秒 → 最終チェック。初期化か整合性チェックに失敗すると即 FAIL
- 打ち切り: クリティカルエラー(長時間マッチされないライド、誤った支払い、評価済みで未決済のライドなど)、200 件以上のワーニング
- 猶予: 椅子座標の反映 3 秒、マッチング通知 30 秒。タイムアウト: notification 系 60 秒、initialize 30 秒、その他 10 秒
- 負荷終了時点で進行中の決済は終了後 5 秒以内に完了する必要がある
- `/api/internal/matching` と `isuride-matcher.service` は仕様を自由に変えてよい
- 追試: 3台を順不同で再起動して走行する。再起動後に動かない、envcheck 失敗、スコアが 75% 以下が続く、などで失格
- 禁止: `envcheck.service` / `/opt/isucon-env-checker` / `isuadmin` の変更、22 / 443 の SG 変更、外部リソースへの処理の委譲
