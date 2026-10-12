#!/bin/bash

# ログ取得先は HOSTS の先頭ホスト(未設定なら i1)
: "${HOSTS:=i1}"

# 現在のブランチ名を取得
CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD)
# origin にブランチがあるか確認
if ! git ls-remote --exit-code --heads origin "$CURRENT_BRANCH"; then
  echo "origin にブランチ $CURRENT_BRANCH が見つかりません。Pushしてください。"
  exit 1
fi

# 現在のブランチに紐付いたPRを取得
PR_NUMBER=$(gh pr list --head "$CURRENT_BRANCH" --json number -q '.[0].number')
# PRが存在するか確認
if [ -z "$PR_NUMBER" ]; then
  echo "PRが見つかりません。新しいPRを作成します。"
  gh pr create --title "[SCORE] 変更内容" --body ""
  echo "完了しました。"
fi
PR_NUMBER=$(gh pr list --head "$CURRENT_BRANCH" --json number -q '.[0].number')
[ -n "$PR_NUMBER" ] || { echo "PR 番号を取得できませんでした" >&2; exit 1; }


echo "NALPを取得します。"
NALP=$(ssh "${HOSTS%% *}" -A "cd webapp && make nalp")
echo "完了しました。"

echo "PTを取得します。"
PT=$(ssh "${HOSTS%% *}" -A "cd webapp && make pt && cat ~/pt.log" | LC_CTYPE=C cut -c 1-300 | awk '{ if (length($0) >= 300) print $0 "........."; else print $0 }')
echo "完了しました。"

# コメントとして投稿するメッセージ
COMMENT=$(cat <<EOF
## ALP
\`\`\`
$NALP
\`\`\`

## pt-query-digest
\`\`\`
$PT
\`\`\`
EOF
)

# PRにコメントを投稿
echo "PR #$PR_NUMBER にコメントを追加します"
gh pr comment "$PR_NUMBER" --body "$COMMENT"
echo "完了しました。"
