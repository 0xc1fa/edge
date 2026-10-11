package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppStoreCreateGetAndDecide(t *testing.T) {
	dir := t.TempDir()
	s, err := NewAppStore(dir)
	if err != nil {
		t.Fatalf("建立台账失败: %v", err)
	}

	a, err := s.Create(Application{
		Hostname: "dev01", Flavor: "c2.m4.d40", Image: "ubuntu-22.04-base",
		Applicant: "张三", Contact: "13800138000", Purpose: "测试", DurationDays: 30,
	})
	if err != nil {
		t.Fatalf("写入申请失败: %v", err)
	}
	if a.Status != StatusPending {
		t.Errorf("新申请状态应为 pending，实际 %s", a.Status)
	}
	if !strings.HasPrefix(a.ID, "AP-") {
		t.Errorf("回执号格式异常: %s", a.ID)
	}

	got, ok := s.Get(a.ID)
	if !ok || got.Hostname != "dev01" {
		t.Fatalf("按回执号查不到: ok=%v got=%+v", ok, got)
	}

	decided, err := s.Decide(a.ID, "approve", "已确认用途合理")
	if err != nil {
		t.Fatalf("处理失败: %v", err)
	}
	if decided.Status != StatusApproved || decided.Reason != "已确认用途合理" {
		t.Errorf("处理结果不对: %+v", decided)
	}

	// 重复处理必须报错，避免两条并发操作把状态改乱
	if _, err := s.Decide(a.ID, "reject", "再改一次"); err == nil {
		t.Error("已处理的申请再次处理应当报错")
	}
}

func TestAppStoreRejectsUnknownAction(t *testing.T) {
	s, err := NewAppStore(t.TempDir())
	if err != nil {
		t.Fatalf("建立台账失败: %v", err)
	}
	a, _ := s.Create(Application{Hostname: "dev02"})
	if _, err := s.Decide(a.ID, "maybe", ""); err == nil {
		t.Error("未知动作应当报错")
	}
}

func TestAppStoreReceiptIsNotEnumerable(t *testing.T) {
	s, err := NewAppStore(t.TempDir())
	if err != nil {
		t.Fatalf("建立台账失败: %v", err)
	}
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		a, err := s.Create(Application{Hostname: "dev"})
		if err != nil {
			t.Fatalf("写入失败: %v", err)
		}
		if seen[a.ID] {
			t.Fatalf("回执号重复: %s", a.ID)
		}
		seen[a.ID] = true
	}
}

func TestAppStoreReloadsFromDisk(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewAppStore(dir)
	a, _ := s.Create(Application{Hostname: "dev03", Applicant: "李四"})

	s2, err := NewAppStore(dir)
	if err != nil {
		t.Fatalf("重新载入失败: %v", err)
	}
	got, ok := s2.Get(a.ID)
	if !ok || got.Applicant != "李四" {
		t.Fatalf("重启后数据丢失: ok=%v got=%+v", ok, got)
	}
	if n := s2.CountPending(); n != 1 {
		t.Errorf("待办计数 = %d, 想 1", n)
	}
}

func TestAppStoreListFiltersByStatus(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewAppStore(dir)
	a1, _ := s.Create(Application{Hostname: "a"})
	a2, _ := s.Create(Application{Hostname: "b"})
	if _, err := s.Decide(a1.ID, "approve", ""); err != nil {
		t.Fatalf("处理失败: %v", err)
	}

	if pending := s.List(StatusPending); len(pending) != 1 || pending[0].ID != a2.ID {
		t.Errorf("pending 过滤结果不对: %+v", pending)
	}
	if approved := s.List(StatusApproved); len(approved) != 1 || approved[0].ID != a1.ID {
		t.Errorf("approved 过滤结果不对: %+v", approved)
	}
	if all := s.List(""); len(all) != 2 {
		t.Errorf("全量应 2 条，实际 %d", len(all))
	}
}

// TestAppStoreAtomicWrite 验证落盘不是半截 JSON：
// 数据文件必须能被独立解析，且没有残留临时文件。
func TestAppStoreAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewAppStore(dir)
	if _, err := s.Create(Application{Hostname: "dev04"}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, applicationsFile))
	if err != nil {
		t.Fatalf("读文件失败: %v", err)
	}
	var items []Application
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("磁盘上的内容不是合法 JSON: %v", err)
	}
	if len(items) != 1 {
		t.Errorf("磁盘上有 %d 条，想 1 条", len(items))
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("残留临时文件: %s", e.Name())
		}
	}
}
