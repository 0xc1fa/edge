package store

import (
	"testing"
	"time"
)

func newGPUStore(t *testing.T) *GPUStore {
	t.Helper()
	s, err := NewGPUStore(t.TempDir())
	if err != nil {
		t.Fatalf("建预约台账失败: %v", err)
	}
	return s
}

// TestGPUBookingDayConflict 锁住「按天预约」这条规则的核心：
// 同一天的卡不能给两个人。
//
// 关键细节：额度只在**批准之后**才被占住。待处理的申请不占额度 ——
// 两个人同时申请同一天是常事，真正要挡的是「批准给两个人」。
func TestGPUBookingDayConflict(t *testing.T) {
	s := newGPUStore(t)
	now := time.Now()
	today := now.Format("2006-01-02")

	first, err := s.Create(GPUInput{
		Instance: "tenant01", Applicant: "张三", Purpose: "训练", Date: today,
	}, 1, now)
	if err != nil {
		t.Fatalf("第一条预约不该失败: %v", err)
	}
	// 还挂着 pending 时，第二个人也可以提 —— 这是申请，不是占用
	if _, err := s.Create(GPUInput{
		Instance: "tenant02", Applicant: "李四", Purpose: "推理", Date: today,
	}, 1, now); err != nil {
		t.Fatalf("待处理阶段不该互相挡住: %v", err)
	}
	if _, err := s.Decide(first.ID, "approve", "", "管理员", 1, now); err != nil {
		t.Fatalf("批准第一条失败: %v", err)
	}
	// 第一条已经占住这天，再申请就应当被明确拒绝
	if _, err := s.Create(GPUInput{
		Instance: "tenant03", Applicant: "王五", Purpose: "训练", Date: today,
	}, 1, now); err == nil {
		t.Fatal("这一天已经被批走了，还能再约")
	}

	// 换一天就没问题：冲突的粒度是天，不是人
	tomorrow := now.AddDate(0, 0, 1).Format("2006-01-02")
	if _, err := s.Create(GPUInput{
		Instance: "tenant03", Applicant: "王五", Purpose: "训练", Date: tomorrow,
	}, 1, now); err != nil {
		t.Errorf("第二天的预约不该被第一天的占用挡住: %v", err)
	}
}

// TestGPUBookingPastDate 过去的日子不能约。
// 这不是洁癖：预约是一件要执行的事（挂卡、到期摘卡），
// 给它一个已经过去的时间，执行端就只能靠猜。
func TestGPUBookingPastDate(t *testing.T) {
	s := newGPUStore(t)
	now := time.Now()
	if _, err := s.Create(GPUInput{
		Instance: "tenant01", Applicant: "张三", Date: now.AddDate(0, 0, -1).Format("2006-01-02"),
	}, 1, now); err == nil {
		t.Fatal("过去的日期被接受了")
	}
	if _, err := s.Create(GPUInput{
		Instance: "tenant01", Applicant: "张三", Date: now.AddDate(0, 0, 31).Format("2006-01-02"),
	}, 1, now); err == nil {
		t.Fatal("超过 30 天的日期被接受了")
	}
}

// TestGPUFlow 走一遍完整生命周期：批准 → 挂载 → 摘卡。
// 分开成三步的意义在于「批准失败」和「挂载失败」是两件不同的事，
// 合在一起的话，一次设备层的失败会把审批结论也弄脏。
func TestGPUFlow(t *testing.T) {
	s := newGPUStore(t)
	now := time.Now()
	b, err := s.Create(GPUInput{Instance: "tenant01", Applicant: "张三", Date: now.Format("2006-01-02")}, 1, now)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if b.Status != GPUPending {
		t.Fatalf("新预约应当是待处理，实际 %s", b.Status)
	}

	// 没批准就挂载必须被拒：否则批准这道关口形同虚设
	if _, err := s.MarkAttached(b.ID, "gpu0", now); err == nil {
		t.Fatal("未批准的预约也能挂载")
	}

	b, err = s.Decide(b.ID, "approve", "", "管理员", 1, now)
	if err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	if b.Status != GPUApproved {
		t.Fatalf("批准后状态应为 approved，实际 %s", b.Status)
	}

	b, err = s.MarkAttached(b.ID, "gpu0", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("挂载失败: %v", err)
	}
	if b.Status != GPUActive || b.Device != "gpu0" || b.AttachedAt == "" {
		t.Fatalf("挂载后的记录不对: %+v", b)
	}
	if got := len(s.ListActive()); got != 1 {
		t.Errorf("正在挂卡的应当只有这一条，实际 %d", got)
	}

	b, err = s.MarkDetached(b.ID, "时段结束，自动摘卡", now.Add(8*time.Hour))
	if err != nil {
		t.Fatalf("摘卡失败: %v", err)
	}
	if b.Status != GPUEnded || b.DetachedAt == "" {
		t.Fatalf("摘卡后的记录不对: %+v", b)
	}
	if got := len(s.ListActive()); got != 0 {
		t.Errorf("摘完就不该再有 active 的了，实际 %d", got)
	}
}

// TestGPUDecideConflict 覆盖「提交到批准之间库存变了」这种窗口：
// 两个人同一天提交（此时都还在 pending，不占额度），
// 批准第一条之后，第二条再批准就必须失败。
func TestGPUDecideConflict(t *testing.T) {
	s := newGPUStore(t)
	now := time.Now()
	date := now.Format("2006-01-02")
	// 用 available=2 让两条都进得来（pending 不占额度，本来就不冲突，
	// 这里想测的是批准那一刻的库存只有 1）
	a, _ := s.Create(GPUInput{Instance: "t1", Applicant: "张三", Date: date}, 2, now)
	b, _ := s.Create(GPUInput{Instance: "t2", Applicant: "李四", Date: date}, 2, now)

	if _, err := s.Decide(a.ID, "approve", "", "管理员", 1, now); err != nil {
		t.Fatalf("第一条批准不该失败: %v", err)
	}
	if _, err := s.Decide(b.ID, "approve", "", "管理员", 1, now); err == nil {
		t.Fatal("库存只剩一张时，第二条批准也成功了")
	}
}

// TestGPUWindow 时段是固定的 9 点到 16 点。
// 写死在测试里是因为到期摘卡的时间点完全由它决定：
// 这两个常量一动，卡被摘走的时刻就跟着变。
func TestGPUWindow(t *testing.T) {
	start, end, ok := GPUWindow("2026-10-17")
	if !ok {
		t.Fatal("日期合法却算不出时段")
	}
	if start.Hour() != 9 || start.Minute() != 0 {
		t.Errorf("时段应当从 9:00 开始，实际 %s", start.Format("15:04"))
	}
	if end.Hour() != 16 {
		t.Errorf("时段应当在 16:00 结束，实际 %s", end.Format("15:04"))
	}
	// 9:00–16:00 是 7 小时。这里按时刻算而不是按某个「应当几小时」的想象，
	// 免得有人改了常量而测试还在说 8 小时 —— 那才是真正的偏差。
	if got := end.Sub(start); got != time.Duration(GPUWindowEndHour-GPUWindowStartHour)*time.Hour {
		t.Errorf("时段长度与常量不符: %v", got)
	}
	// 日期按本地时区解释：容器里是 Asia/Shanghai，
	// 与管理员在页面上看到的「今天」必须是同一个
	if start.Format("2006-01-02") != "2026-10-17" {
		t.Errorf("时段起点日期不对: %s", start)
	}
	if _, _, ok := GPUWindow("不是日期"); ok {
		t.Error("非法日期不应当算出时段")
	}
}

// TestGPUCancelOnlyBeforeAttach 已在使用的预约不能「取消」了事 ——
// 那会在台账上留下一条没有结束时刻的记录，
// 后面的人看排期时会以为那张卡还挂着。
func TestGPUCancelOnlyBeforeAttach(t *testing.T) {
	s := newGPUStore(t)
	now := time.Now()
	b, _ := s.Create(GPUInput{Instance: "t1", Applicant: "张三", Date: now.Format("2006-01-02")}, 1, now)
	if _, err := s.Cancel(b.ID, "张三", now); err != nil {
		t.Fatalf("挂卡之前应当可以取消: %v", err)
	}

	b2, _ := s.Create(GPUInput{Instance: "t1", Applicant: "张三", Date: now.AddDate(0, 0, 1).Format("2006-01-02")}, 1, now)
	s.Decide(b2.ID, "approve", "", "管理员", 1, now)
	s.MarkAttached(b2.ID, "gpu0", now)
	if _, err := s.Cancel(b2.ID, "管理员", now); err == nil {
		t.Fatal("已经在用的预约不该能直接取消，应当先摘卡")
	}
}
