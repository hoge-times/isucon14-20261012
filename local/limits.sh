#!/bin/sh
# 全サービスをまとめる親 cgroup(isuride-local)を Docker Desktop の VM に作り、i1 と同じ資源の上限をかける。
# compose.yml の limits サービスが起動のたびに実行する。CPUS / MEMORY は Makefile から渡る。
# 動いているコンテナにもその場で効く。
set -eu

cg=/sys/fs/cgroup/isuride-local
mkdir -p "$cg"
echo "+cpuset +cpu +io +memory +pids" > "$cg/cgroup.subtree_control"
echo "$CPUS" > "$cg/cpuset.cpus"
echo "$MEMORY" > "$cg/memory.max"
# EC2 と同じくスワップ無しにする(メモリを使い切ったら OOM で落ちる)
if [ -f "$cg/memory.swap.max" ]; then
  echo 0 > "$cg/memory.swap.max"
fi

echo "isuride-local: cpuset.cpus=$(cat "$cg/cpuset.cpus.effective") memory.max=$(cat "$cg/memory.max")"
