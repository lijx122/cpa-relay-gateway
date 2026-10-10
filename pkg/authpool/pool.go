package authpool

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

// AuthRecord 代表从本地 json 凭据文件中解析出的认证凭据
type AuthRecord struct {
	FilePath     string    `json:"-"`
	Type         string    `json:"type"` // "xai", "codex", "antigravity"
	Email        string    `json:"email"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	AccountID    string    `json:"account_id"` // Codex 特有
	ProjectID    string    `json:"project_id"` // Antigravity 特有
	BaseURL      string    `json:"base_url"`
	Disabled     bool      `json:"disabled"`
	Expired      time.Time `json:"expired"`
}

// Pool 管理某一类型或某一目录下的凭据池，支持 Round-Robin 调度与文件监听
type Pool struct {
	dir      string
	authType string
	mu       sync.RWMutex
	records  []*AuthRecord
	rrIndex  uint64
}

func NewPool(dir string, authType string) (*Pool, error) {
	p := &Pool{
		dir:      dir,
		authType: strings.ToLower(authType),
	}
	if err := p.reload(); err != nil {
		return nil, err
	}
	return p, nil
}

// Reload 重新扫描目录并加载合法未禁用的凭据
func (p *Pool) reload() error {
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		return fmt.Errorf("read auth dir %s: %w", p.dir, err)
	}

	var loaded []*AuthRecord
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		fullPath := filepath.Join(p.dir, entry.Name())
		data, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}

		var rec AuthRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			continue
		}
		rec.FilePath = fullPath

		// 类型匹配（若未指定或匹配目标类型）
		recType := strings.ToLower(rec.Type)
		if p.authType != "" && recType != p.authType {
			continue
		}

		if rec.Disabled || rec.AccessToken == "" {
			continue
		}

		loaded = append(loaded, &rec)
	}

	p.mu.Lock()
	p.records = loaded
	p.mu.Unlock()

	log.Printf("[INFO] [authpool:%s] loaded %d active credentials from %s", p.authType, len(loaded), p.dir)
	return nil
}

// Next 获取下一个可用的轮询凭据
func (p *Pool) Next() (*AuthRecord, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if len(p.records) == 0 {
		return nil, fmt.Errorf("no active credentials available in pool %q", p.authType)
	}

	idx := atomic.AddUint64(&p.rrIndex, 1) - 1
	selected := p.records[idx%uint64(len(p.records))]
	return selected, nil
}

// Count 获取当前池中凭据总数
func (p *Pool) Count() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.records)
}

// StartWatcher 启动后台文件监听与热更新
func (p *Pool) StartWatcher(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	if err := watcher.Add(p.dir); err != nil {
		_ = watcher.Close()
		return err
	}

	go func() {
		defer watcher.Close()
		debounceTimer := time.NewTimer(0)
		if !debounceTimer.Stop() {
			<-debounceTimer.C
		}

		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if strings.HasSuffix(event.Name, ".json") {
					debounceTimer.Reset(100 * time.Millisecond)
				}
			case <-debounceTimer.C:
				_ = p.reload()
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("[WARN] [authpool:%s] watcher error: %v", p.authType, err)
			}
		}
	}()

	return nil
}
