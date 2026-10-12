# アプリと payment_mock の実行用イメージ。本番と同じ Ubuntu 24.04 にする。
# sql/init.sh が mysql クライアントと gzip を使うので入れておく。バイナリはイメージに含めない(compose でマウントする)。
FROM ubuntu:24.04

RUN apt-get update \
 && apt-get install -y --no-install-recommends mysql-client-core-8.0 ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && useradd -m -s /bin/bash isucon
