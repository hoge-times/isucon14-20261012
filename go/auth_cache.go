package main

import (
	"context"
	"sync"
)

// 認証のたびに users / chairs を access_token で SELECT していたのを、メモリから引く。
// 初めて見たトークンだけ DB を引いて覚える(リードスルー)。users は書き換えられない。
// chairs は is_active だけが chairPostActivity で変わるので、DB に書いた後でここも直す
type authCache struct {
	mu sync.RWMutex
	// access_token → users の行
	users map[string]User
	// access_token → chairs の行
	chairs map[string]Chair
}

var authCaches = newAuthCache()

func newAuthCache() *authCache {
	return &authCache{
		users:  map[string]User{},
		chairs: map[string]Chair{},
	}
}

// init.sh でデータを入れ直したら、前の走行で覚えた行を捨てる
func (c *authCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.users = map[string]User{}
	c.chairs = map[string]Chair{}
}

// 見つからなければ sql.ErrNoRows を返す。無効なトークンは覚えない
func (c *authCache) getUser(ctx context.Context, q executableGet, accessToken string) (*User, error) {
	c.mu.RLock()
	u, ok := c.users[accessToken]
	c.mu.RUnlock()
	if ok {
		return &u, nil
	}

	if err := q.GetContext(ctx, &u, "SELECT * FROM users WHERE access_token = ?", accessToken); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if cached, ok := c.users[accessToken]; ok {
		u = cached
	} else {
		c.users[accessToken] = u
	}
	c.mu.Unlock()
	return &u, nil
}

// 見つからなければ sql.ErrNoRows を返す。無効なトークンは覚えない
func (c *authCache) getChair(ctx context.Context, q executableGet, accessToken string) (*Chair, error) {
	c.mu.RLock()
	ch, ok := c.chairs[accessToken]
	c.mu.RUnlock()
	if ok {
		return &ch, nil
	}

	if err := q.GetContext(ctx, &ch, "SELECT * FROM chairs WHERE access_token = ?", accessToken); err != nil {
		return nil, err
	}
	// SELECT と chairPostActivity が競ったとき、先に入った新しい is_active を古い読み取りで上書きしないよう、空いているときだけ入れる
	c.mu.Lock()
	if cached, ok := c.chairs[accessToken]; ok {
		ch = cached
	} else {
		c.chairs[accessToken] = ch
	}
	c.mu.Unlock()
	return &ch, nil
}

// chairPostActivity が DB を更新した後に呼ぶ。まだ覚えていない椅子でも入れておく(次の認証で古い値を読まないように)
func (c *authCache) setChairActive(chair Chair, isActive bool) {
	chair.IsActive = isActive
	c.mu.Lock()
	defer c.mu.Unlock()
	c.chairs[chair.AccessToken] = chair
}
