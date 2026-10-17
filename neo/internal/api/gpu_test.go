package api

import (
	"testing"
	"time"

	"neo/internal/store"
)

// TestGPUMountedBeforeBoot 钉住「要不要重启」的判据：
// 看的是容器**本次启动时**这台机器带着卡没有，而不是此刻有没有卡、
// 也不是 nvidia.runtime 开着没有 —— runtime 开着却从没重启过的实例，照样看不见卡。
//
// 这条来自一次实机对照：同一个 PID 下，摘掉设备 /dev/nvidia0 立刻消失，
// 插回去两秒内 nvidia-smi 就认得卡；而给一台从未挂过卡的运行实例挂上去，
// 不重启就是看不见。差别只在那次启动时有没有 GPU 设备。
func TestGPUMountedBeforeBoot(t *testing.T) {
	boot := time.Date(2026, 10, 17, 11, 0, 0, 0, time.Local)
	mounted := func(at time.Time, status string) store.GPUBooking {
		b := store.GPUBooking{
			Instance: "tenant01", Status: status,
		}
		if !at.IsZero() {
			b.AttachedAt = at.Format(time.RFC3339)
		}
		return b
	}

	cases := []struct {
		name    string
		items   []store.GPUBooking
		bootAt  time.Time
		wantOK  bool // true=容器带着卡启动，挂载后不必重启
	}{
		{"从来没挂过卡 → 冷启动", []store.GPUBooking{}, boot, false},
		{"只有一条没挂载成功的记录", []store.GPUBooking{mounted(time.Time{}, store.GPUApproved)}, boot, false},
		{
			"上次挂载早于本次启动 → 热插生效",
			[]store.GPUBooking{mounted(boot.Add(-30*time.Minute), store.GPUEnded)},
			boot, true,
		},
		{
			"上次挂载晚于本次启动（挂了没重启） → 仍要重启",
			[]store.GPUBooking{mounted(boot.Add(30*time.Minute), store.GPUActive)},
			boot, false,
		},
		{
			"多条里有一条早于启动 → 那次注入过钩子",
			[]store.GPUBooking{
				mounted(boot.Add(2*time.Hour), store.GPUActive),   // 最近一次：挂在启动后
				mounted(boot.Add(-24*time.Hour), store.GPUEnded),  // 更早那次：挂完之后重启过
			},
			boot, true,
		},
		{
			"别的实例的挂载不算数",
			[]store.GPUBooking{{Instance: "tenant02", Status: store.GPUEnded,
				AttachedAt: boot.Add(-24 * time.Hour).Format(time.RFC3339)}},
			boot, false,
		},
		{"拿不到启动时刻 → 保守地要重启", []store.GPUBooking{mounted(boot.Add(-24 * time.Hour), store.GPUEnded)}, time.Time{}, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := gpuMountedBeforeBoot("tenant01", c.items, c.bootAt); got != c.wantOK {
				t.Errorf("gpuMountedBeforeBoot = %v，想 %v", got, c.wantOK)
			}
		})
	}
}

// TestGPUWantsRestart 覆盖两个信号的组合。
// 重点是第二种：台账记录被删掉（清理测试数据是正经操作）之后，
// 不能把「明明能热插」判成「必须重启」—— 那是白白打断租户一次。
func TestGPUWantsRestart(t *testing.T) {
	boot := time.Date(2026, 10, 17, 10, 40, 0, 0, time.Local)
	old := []store.GPUBooking{{
		Instance:   "tenant01",
		Status:     store.GPUEnded,
		AttachedAt: boot.Add(-2 * time.Hour).Format(time.RFC3339),
	}}

	cases := []struct {
		name         string
		hadGPUDevice bool
		items        []store.GPUBooking
		bootAt       time.Time
		wantRestart  bool
	}{
		{"挂载前已经有卡 → 热插", true, nil, boot, false},
		{"台账历史早于启动 → 热插", false, old, boot, false},
		{"记录被删干净了，只好看启动时刻 → 保守要", false, nil, boot, true},
		{"没有历史、也没启动时刻 → 保守要", false, nil, time.Time{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := gpuWantsRestart(c.hadGPUDevice, "tenant01", c.items, c.bootAt)
			if got != c.wantRestart {
				t.Errorf("是否要重启 = %v，想 %v", got, c.wantRestart)
			}
		})
	}
}

// TestGPUVisibleToMemberOnlyAfterRealSchedule 钉住「这一页对成员是调度日志」这条口径。
//
// 成员看到的每一条都必须是卡真的挂上去过的。没挂过的记录对他有两个坏处：
// 一是他既批不了也挂不上，看见「已批准」只会以为卡已经到手了；
// 二是那些记录里混着测试与误填的假数据，摆在页面上就是在骗人。
//
// 这条边界放在服务端：前端藏起来的东西，接口照样会给出去。
func TestGPUVisibleToMemberOnlyAfterRealSchedule(t *testing.T) {
	now := time.Now()
	scheduled := store.GPUBooking{
		ID:         "GU-1",
		Instance:   "tenant01",
		Status:     store.GPUEnded,
		AttachedAt: now.Format(time.RFC3339),
		DetachedAt: now.Add(time.Hour).Format(time.RFC3339),
	}
	cases := []struct {
		name    string
		booking store.GPUBooking
		want    bool
	}{
		{"挂过卡的：已结束", scheduled, true},
		{"挂过卡的：正在用", func() store.GPUBooking {
			b := scheduled
			b.Status = store.GPUActive
			b.DetachedAt = ""
			return b
		}(), true},
		{"待批准", store.GPUBooking{ID: "GU-2", Status: store.GPUPending}, false},
		{"已批准但还没挂", store.GPUBooking{ID: "GU-3", Status: store.GPUApproved}, false},
		{"已驳回", store.GPUBooking{ID: "GU-4", Status: store.GPURejected}, false},
		{"已取消", store.GPUBooking{ID: "GU-5", Status: store.GPUCancelled}, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := gpuVisible(c.booking, false); got != c.want {
				t.Errorf("成员看到 %s = %v，想 %v", c.booking.Status, got, c.want)
			}
			// 管理员什么都要看得到：他要点批准、点挂载，看不到待办就没法干活
			if got := gpuVisible(c.booking, true); !got {
				t.Errorf("管理员看不到 %s，待办就没法处理了", c.booking.Status)
			}
		})
	}
}
