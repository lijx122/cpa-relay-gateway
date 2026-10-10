package authpool

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAuthPoolLoadAndRoundRobin(t *testing.T) {
	tempDir := t.TempDir()

	file1 := filepath.Join(tempDir, "xai-user1.json")
	_ = os.WriteFile(file1, []byte(`{
		"type": "xai",
		"email": "user1@example.com",
		"access_token": "token-user1",
		"disabled": false
	}`), 0644)

	file2 := filepath.Join(tempDir, "xai-user2.json")
	_ = os.WriteFile(file2, []byte(`{
		"type": "xai",
		"email": "user2@example.com",
		"access_token": "token-user2",
		"disabled": false
	}`), 0644)

	// 一个被禁用的账号，不应载入
	fileDisabled := filepath.Join(tempDir, "xai-user3.json")
	_ = os.WriteFile(fileDisabled, []byte(`{
		"type": "xai",
		"email": "user3@example.com",
		"access_token": "token-user3",
		"disabled": true
	}`), 0644)

	pool, err := NewPool(tempDir, "xai")
	if err != nil {
		t.Fatalf("NewPool failed: %v", err)
	}

	if pool.Count() != 2 {
		t.Fatalf("expected 2 active credentials, got %d", pool.Count())
	}

	// 测试 Round-Robin
	c1, err := pool.Next()
	if err != nil {
		t.Fatalf("pool.Next failed: %v", err)
	}
	c2, err := pool.Next()
	if err != nil {
		t.Fatalf("pool.Next failed: %v", err)
	}

	if c1.Email == c2.Email {
		t.Errorf("expected different credentials on sequential Next(), got same: %s", c1.Email)
	}
}

func TestAuthPoolHotReload(t *testing.T) {
	tempDir := t.TempDir()

	file1 := filepath.Join(tempDir, "xai-user1.json")
	_ = os.WriteFile(file1, []byte(`{
		"type": "xai",
		"email": "user1@example.com",
		"access_token": "token-user1",
		"disabled": false
	}`), 0644)

	pool, err := NewPool(tempDir, "xai")
	if err != nil {
		t.Fatalf("NewPool failed: %v", err)
	}
	if pool.Count() != 1 {
		t.Fatalf("expected 1 credential, got %d", pool.Count())
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := pool.StartWatcher(ctx); err != nil {
		t.Fatalf("StartWatcher failed: %v", err)
	}

	// 动态写入新账号
	file2 := filepath.Join(tempDir, "xai-user2.json")
	_ = os.WriteFile(file2, []byte(`{
		"type": "xai",
		"email": "user2@example.com",
		"access_token": "token-user2",
		"disabled": false
	}`), 0644)

	time.Sleep(250 * time.Millisecond)

	if pool.Count() != 2 {
		t.Errorf("expected 2 credentials after hot reload, got %d", pool.Count())
	}
}
