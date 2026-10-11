package catalog

import "testing"

func TestDefaultCatalogHasSellableItems(t *testing.T) {
	c := Default()
	if len(c.Flavors) == 0 || len(c.GPUModels) == 0 || len(c.Images) == 0 {
		t.Fatal("默认目录必须有云主机、GPU 型号与镜像")
	}
	sellable := 0
	for _, i := range c.Images {
		if i.Available {
			sellable++
		}
	}
	if sellable == 0 {
		t.Error("至少要有一个可售镜像，否则申请页无从默认值")
	}
}

// TestGPUAlwaysRequiresFlavor 锁死产品规则：GPU 不是独立商品。
// 将来若有人把 RequiresFlavor 关掉，这条测试会立刻提醒「这是产品变更，需要评审」。
func TestGPUAlwaysRequiresFlavor(t *testing.T) {
	for _, m := range Default().GPUModels {
		if !m.RequiresFlavor {
			t.Errorf("%s 必须标注 requires_flavor，GPU 不能脱离云主机独立售卖", m.ID)
		}
	}
}

// TestZeroStockNeverSellable 是这个产品的重要护栏：
// 库存为 0 的型号（RTX PRO 6000）必须永远不可售，且理由固定为「待上架」——
// 与「已被全部占用」区分开：前者是还没上货架，后者是上过但卖完了。
func TestZeroStockNeverSellable(t *testing.T) {
	out := EvaluateGPUs(Default().GPUModels, Capacity{GPUAllocatedByModel: map[string]int{}})
	for _, m := range out {
		if m.Total <= 0 {
			if m.Available {
				t.Errorf("%s 库存为 0 却被标成可售", m.ID)
			}
			if m.Reason != "待上架" {
				t.Errorf("%s 零库存的对外表述应为「待上架」，实际: %q", m.ID, m.Reason)
			}
		}
	}
}

func TestEvaluateGPUSoldOutByModel(t *testing.T) {
	c := Default()
	// 4090 两张都被占（含待处理申请的预占）
	out := EvaluateGPUs(c.GPUModels, Capacity{GPUAllocatedByModel: map[string]int{"rtx-4090": 2}})
	for _, m := range out {
		if m.ID == "rtx-4090" && m.Available {
			t.Error("4090 已被占满却仍标可售")
		}
		if m.ID == "rtx-4090" && m.Reason == "" {
			t.Error("售罄时应给出原因，页面上要展示给申请人")
		}
	}

	// 只占一张：仍可售，但最多只能再配 1 张
	out = EvaluateGPUs(c.GPUModels, Capacity{GPUAllocatedByModel: map[string]int{"rtx-4090": 1}})
	for _, m := range out {
		if m.ID == "rtx-4090" && !m.Available {
			t.Errorf("只占一张时仍应可售，实际 %s", m.Reason)
		}
		if m.ID == "rtx-4090" && m.AvailableCards != 1 {
			t.Errorf("剩一张时可配张数应为 1，实际 %d", m.AvailableCards)
		}
		if m.Total <= 0 && m.AvailableCards != 0 {
			t.Errorf("%s 零库存时可配张数应为 0，实际 %d", m.ID, m.AvailableCards)
		}
	}
}

// TestMaxCardsForRespectsInventoryAndCap 校验可配张数同时受库存与实例上限约束。
func TestMaxCardsForRespectsInventoryAndCap(t *testing.T) {
	m := GPUModel{ID: "x", Total: 2}
	if got := MaxCardsFor(m, 0); got != MaxGPUCards {
		t.Errorf("库存充足时应允许 %d 张，实际 %d", MaxGPUCards, got)
	}
	if got := MaxCardsFor(m, 1); got != 1 {
		t.Errorf("只剩一张时应只能配 1 张，实际 %d", got)
	}
	if got := MaxCardsFor(m, 2); got != 0 {
		t.Errorf("占满时应不可配，实际 %d", got)
	}
	// 宿主装了超过实例上限的卡，也不能一次性全给一台
	many := GPUModel{ID: "y", Total: 8}
	if got := MaxCardsFor(many, 0); got != MaxGPUCards {
		t.Errorf("单实例仍应不超过 %d 张，实际 %d", MaxGPUCards, got)
	}
	// 没装的型号：0 张，不因为 Total<=0 而算出负数
	if got := MaxCardsFor(GPUModel{ID: "z", Total: 0}, 0); got != 0 {
		t.Errorf("零库存型号应不可配，实际 %d", got)
	}
}

func TestFindGPURejectsUnknownModel(t *testing.T) {
	c := Default()
	if _, ok := c.FindGPU(""); !ok {
		t.Error("不配卡（空型号）应视为合法取值")
	}
	if _, ok := c.FindGPU("rtx-9090"); ok {
		t.Error("未上架的型号不应被查到")
	}
}

// TestDefaultGPUTargetExists 保证「认不出型号的已用卡」有地方可归，
// 否则未知占用会被静默丢掉，直接变成超卖。
func TestDefaultGPUTargetExists(t *testing.T) {
	if _, ok := Default().DefaultGPUTarget(); !ok {
		t.Error("必须有一个型号承担无法识别的占用")
	}
}

// TestWithGPUStockOnlyOverridesPracticalModel 保证运维调库存调不出没装的卡。
func TestWithGPUStockOnlyOverridesPracticalModel(t *testing.T) {
	c := Default().WithGPUStock(4)
	var pro GPUModel
	var installed *GPUModel
	for i, m := range c.GPUModels {
		if m.ID == "rtx-pro-6000" {
			pro = m
		}
		if m.DefaultTarget {
			installed = &c.GPUModels[i]
		}
	}
	if pro.Total != 0 {
		t.Errorf("本机没装的型号不该被运维库存覆盖: %d", pro.Total)
	}
	if installed == nil || installed.Total != 4 {
		t.Errorf("实测库存未正确注入: %+v", installed)
	}
	// 配置缺失时保留目录里的实测值，而不是把库存调成 0
	if got := Default().WithGPUStock(0).GPUModels[0].Total; got != 2 {
		t.Errorf("库存配置为 0 应视为「没配」，实际得到 %d", got)
	}
}

func TestDefaultImageIsSellable(t *testing.T) {
	c := Default()
	img, ok := c.DefaultImage()
	if !ok {
		t.Fatal("必须能拿到默认镜像，否则申请人不选镜像时会卡住")
	}
	if !img.Available {
		t.Errorf("默认镜像 %s 不可售，应另选一个可售的", img.ID)
	}
	if img.ID == "ubuntu-22.04-cuda" {
		t.Error("默认镜像不能是筹备中的那个")
	}
}

func TestFindImageRefusesUnavailable(t *testing.T) {
	c := Default()
	if _, ok := c.FindImage("ubuntu-22.04-cuda"); ok {
		t.Error("筹备中的镜像不应被当成可选镜像返回")
	}
	if _, ok := c.FindImage("ubuntu-22.04-base"); !ok {
		t.Error("可售镜像应当能查到")
	}
}

// TestLoginUserIsRoot 第一版只提供 root 登录；将来改成免 root 时这里会提醒
// 「这条要同步改 README 与申请页文案」。
func TestLoginUserIsRoot(t *testing.T) {
	if LoginUser != "root" {
		t.Errorf("默认登录账户应仍是 root，实际 %q", LoginUser)
	}
}

// TestFlavorsHaveDistinctSpecs 防止两档云主机被不小心配成同规格 ——
// 页面上出现两张一模一样的卡是很直接的不专业观感。
func TestFlavorsHaveDistinctSpecs(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range Default().Flavors {
		key := f.Title
		if seen[key] {
			t.Errorf("出现重复且同名的规格: %s", key)
		}
		seen[key] = true
	}
}

func TestEvaluateFlavorsResourceLimits(t *testing.T) {
	f := Flavor{Name: "test", VCPU: 2, MemoryMiB: 4096, DiskGB: 10}

	out := EvaluateFlavors([]Flavor{f}, Capacity{
		CPUTotal: 4, MemoryTotal: 8 * 1024 * 1024 * 1024,
	})
	if !out[0].Available {
		t.Errorf("余量充足应可售: %s", out[0].Reason)
	}

	out = EvaluateFlavors([]Flavor{f}, Capacity{
		CPUTotal: 4, CPUAllocated: 3, MemoryTotal: 8 * 1024 * 1024 * 1024,
	})
	if out[0].Available {
		t.Error("CPU 不足仍标可售")
	}
	if out[0].Reason == "" {
		t.Error("不可售时应给出原因")
	}
}

// TestEvaluateIgnoresUnknownCapacity 覆盖取不到 resources 的情形：
// 整个申请页不能因为没有容量数据就全部变灰。
func TestEvaluateIgnoresUnknownCapacity(t *testing.T) {
	out := EvaluateFlavors([]Flavor{{Name: "test", VCPU: 8, MemoryMiB: 8192}}, Capacity{})
	if !out[0].Available {
		t.Error("宿主容量未知时不应判死所有规格")
	}
}

// TestEvaluateDoesNotMutateOrigin 保证可售标注只发生在返回副本上。
// 若改成原地修改，同一个进程内第二次调用会在上一轮结果上叠加判断。
func TestEvaluateDoesNotMutateOrigin(t *testing.T) {
	c := Default()
	fBefore := make([]Flavor, len(c.Flavors))
	copy(fBefore, c.Flavors)
	gBefore := make([]GPUModel, len(c.GPUModels))
	copy(gBefore, c.GPUModels)

	cap := Capacity{GPUAllocatedByModel: map[string]int{"rtx-4090": 2}}
	_ = EvaluateFlavors(c.Flavors, cap)
	_ = EvaluateGPUs(c.GPUModels, cap)

	for i := range fBefore {
		if c.Flavors[i] != fBefore[i] {
			t.Errorf("规格第 %d 项被原地修改: %+v → %+v", i, fBefore[i], c.Flavors[i])
		}
	}
	for i := range gBefore {
		if c.GPUModels[i] != gBefore[i] {
			t.Errorf("GPU 第 %d 项被原地修改: %+v → %+v", i, gBefore[i], c.GPUModels[i])
		}
	}
}
