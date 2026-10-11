// 申请台账：持久化匿名用户提交的开通申请与管理员的处理结果。
//
// 为什么是 JSON 文件而不是 SQLite：控制台第一版的写入面只有这一处，
// 量级是每天个位数；引入数据库会多一分依赖、多一分升级负担，
// 而 JSON 文件能被宿主直接 cat 排障。真到并发量上来再换，不迟。
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 申请状态流转：pending → approved / rejected
const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusRejected = "rejected"
)

// applicationsFile 落在数据目录下。
const applicationsFile = "applications.json"

// Application 是一条开通申请。
type Application struct {
	ID           string `json:"id"`
	Hostname string `json:"hostname"`
	Flavor   string `json:"flavor"`
	Image    string `json:"image"`
	// GPUModel / GPUCount 是显卡型号与张数，两者都为空表示不配卡。
	// GPU 不是独立产品：台账里它没有单独的「规格」，只能作为云主机的附加项存在。
	GPUModel string `json:"gpu_model"`
	GPUCount int    `json:"gpu_count"`
	// LoginUser 是登录账户，第一版恒为 root；留字段是为了将来支持普通账户。
	LoginUser string `json:"login_user"`
	// SSHPubKey 可选：留空表示由系统下发随机口令。
	SSHPubKey string `json:"ssh_public_key"`
	Applicant    string `json:"applicant"`
	Contact      string `json:"contact"`
	Purpose      string `json:"purpose"`
	DurationDays int    `json:"duration_days"`
	Status       string `json:"status"`
	// Reason 是管理员处理时填写的意见，也会展示给申请人
	Reason    string `json:"reason"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// AppStore 是申请台账。
type AppStore struct {
	mu    sync.Mutex
	path  string
	items []Application
}

// NewAppStore 建立台账并载入已有数据。
func NewAppStore(dir string) (*AppStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建数据目录失败: %w", err)
	}
	s := &AppStore{path: filepath.Join(dir, applicationsFile)}
	if raw, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(raw, &s.items); err != nil {
			return nil, fmt.Errorf("申请台账解析失败，请人工核对 %s: %w", s.path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取申请台账失败: %w", err)
	}
	return s, nil
}

// Create 追加一条申请，返回落盘后的记录（含生成的回执号）。
func (s *AppStore) Create(a Application) (Application, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	a.ID = newReceipt(now)
	a.Status = StatusPending
	a.Reason = ""
	a.CreatedAt = now.Format(time.RFC3339)
	a.UpdatedAt = a.CreatedAt

	s.items = append(s.items, a)
	if err := s.saveLocked(); err != nil {
		// 落盘失败要回滚内存状态，否则页面显示的和磁盘上的不一致
		s.items = s.items[:len(s.items)-1]
		return Application{}, err
	}
	return a, nil
}

// Get 按回执号查询。
func (s *AppStore) Get(id string) (Application, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.items {
		if a.ID == id {
			return a, true
		}
	}
	return Application{}, false
}

// List 返回全部申请，最新的排前面。status 为空表示不过滤。
func (s *AppStore) List(status string) []Application {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Application, 0, len(s.items))
	for _, a := range s.items {
		if status == "" || a.Status == status {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

// CountPending 供健康检查展示待办数量。
func (s *AppStore) CountPending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, a := range s.items {
		if a.Status == StatusPending {
			n++
		}
	}
	return n
}

// Decide 管理员处理申请。返回更新后的记录。
func (s *AppStore) Decide(id, action, reason string) (Application, error) {
	var status string
	switch action {
	case "approve":
		status = StatusApproved
	case "reject":
		status = StatusRejected
	default:
		return Application{}, fmt.Errorf("未知的处理动作: %s", action)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.items {
		if s.items[i].ID != id {
			continue
		}
		if s.items[i].Status != StatusPending {
			return Application{}, fmt.Errorf("该申请已被处理（当前状态 %s）", s.items[i].Status)
		}
		s.items[i].Status = status
		s.items[i].Reason = reason
		s.items[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := s.saveLocked(); err != nil {
			// 同样回滚，避免界面显示已处理而磁盘未生效
			s.items[i].Status = StatusPending
			s.items[i].Reason = ""
			return Application{}, err
		}
		return s.items[i], nil
	}
	return Application{}, errors.New("申请不存在")
}

// saveLocked 原子写：先落临时文件再 rename，避免写一半断电留下坏 JSON。
// 调用方必须持有锁。
func (s *AppStore) saveLocked() error {
	raw, err := json.MarshalIndent(s.items, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// receiptAlphabet 去掉了 0/O/1/I 这类容易看错行字符。
const receiptAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

// newReceipt 生成形如 AP-20261010-7XK4 的回执号：带日期便于人工沟通，
// 4 位随机段（32^4 ≈ 100 万）让回执号不可枚举。
func newReceipt(t time.Time) string {
	var sb strings.Builder
	sb.WriteString("AP-")
	sb.WriteString(t.Format("20060102"))
	sb.WriteByte('-')
	n := big.NewInt(int64(len(receiptAlphabet)))
	for i := 0; i < 4; i++ {
		v, err := rand.Int(rand.Reader, n)
		if err != nil {
			// crypto/rand 不可用时退化为时间戳 hash，宁丑勿漏
			sb.WriteString(strings.ToUpper(hex.EncodeToString([]byte{byte(t.Nanosecond() + i)}))[:1])
			continue
		}
		sb.WriteByte(receiptAlphabet[v.Int64()])
	}
	return sb.String()
}
