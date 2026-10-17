// 实例归属台账：哪台机器归谁。
//
// 为什么单独一张表而不是从申请台账推导：申请台账只覆盖「通过控制台
// 申请开通」的实例，而宿主上早就存在手工建的机器。没有这张表，
// 那些实例永远查不到属主，租户侧的网页终端、改口令、开关机
// 对它们就是一堆点不动的按钮。
//
// 也不往 Incus 的实例 config 里塞：那是宿主的配置，不是我们的台账 ——
// 两边各存一份，迟早对不上，而对不上的时候没有谁能当准。
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const ownersFile = "instance-owners.json"

// InstanceOwner 是一条归属登记。
type InstanceOwner struct {
	Instance   string `json:"instance"`
	Owner      string `json:"owner"`
	AssignedBy string `json:"assigned_by"`  // 谁分配的（管理员）
	AssignedAt string `json:"assigned_at"`  // 什么时候分的
}

// OwnerStore 是归属台账。
type OwnerStore struct {
	mu    sync.Mutex
	path  string
	items []InstanceOwner
}

// NewOwnerStore 建立台账并载入已有数据。
func NewOwnerStore(dir string) (*OwnerStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建数据目录失败: %w", err)
	}
	s := &OwnerStore{path: filepath.Join(dir, ownersFile)}
	if raw, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(raw, &s.items); err != nil {
			return nil, fmt.Errorf("实例归属台账解析失败，请人工核对 %s: %w", s.path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取实例归属台账失败: %w", err)
	}
	return s, nil
}

// Set 把一台实例分给某个人。重复分配就是改归属 ——
// 那正是「换人了」的正常路径，不该要求先解除再分配。
func (s *OwnerStore) Set(instance, owner, by string, now time.Time) (InstanceOwner, error) {
	instance = trimSpace(instance)
	owner = trimSpace(owner)
	if instance == "" {
		return InstanceOwner{}, errors.New("实例名为空")
	}
	if owner == "" {
		return InstanceOwner{}, errors.New("归属者为空")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rec := InstanceOwner{
		Instance:   instance,
		Owner:      owner,
		AssignedBy: by,
		AssignedAt: now.Format(time.RFC3339),
	}
	for i := range s.items {
		if s.items[i].Instance == instance {
			prev := s.items[i]
			s.items[i] = rec
			if err := s.saveLocked(); err != nil {
				s.items[i] = prev
				return InstanceOwner{}, err
			}
			return rec, nil
		}
	}
	s.items = append(s.items, rec)
	if err := s.saveLocked(); err != nil {
		s.items = s.items[:len(s.items)-1]
		return InstanceOwner{}, err
	}
	return rec, nil
}

// Get 查一台实例的归属。第二返回值表示有没有显式登记过。
func (s *OwnerStore) Get(instance string) (InstanceOwner, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.items {
		if r.Instance == instance {
			return r, true
		}
	}
	return InstanceOwner{}, false
}

// Delete 解除归属。没登记过时不算错：目标是「这台机器没有显式归属」，
// 而这个结果已经成立了。
func (s *OwnerStore) Delete(instance string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.items {
		if r.Instance != instance {
			continue
		}
		rest := make([]InstanceOwner, 0, len(s.items)-1)
		rest = append(rest, s.items[:i]...)
		rest = append(rest, s.items[i+1:]...)
		prev := s.items
		s.items = rest
		if err := s.saveLocked(); err != nil {
			s.items = prev
			return err
		}
		return nil
	}
	return nil
}

// List 返回全部登记，按实例名排序，便于人工核对。
func (s *OwnerStore) List() []InstanceOwner {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]InstanceOwner, len(s.items))
	copy(out, s.items)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Instance < out[j-1].Instance; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func (s *OwnerStore) saveLocked() error {
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

func trimSpace(v string) string {
	start, end := 0, len(v)
	for start < end && (v[start] == ' ' || v[start] == '\t' || v[start] == '\n' || v[start] == '\r') {
		start++
	}
	for end > start && (v[end-1] == ' ' || v[end-1] == '\t' || v[end-1] == '\n' || v[end-1] == '\r') {
		end--
	}
	return v[start:end]
}
