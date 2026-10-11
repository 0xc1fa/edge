// Package recon 周期性地从 Incus 拉取事实，并对外提供只读快照。
//
// 这一层存在的意义：API 处理请求时不再直连 Incus。
// 页面刷新是高频动作，而 incus socket 往返有成本；同时把「多久没成功取到数据」
// 变成一个可判定的健康信号。
package recon

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"neo/internal/incus"
)

// Sample 是一个采样点，喂给前端的迷你曲线。
type Sample struct {
	TS         int64   `json:"ts"`
	CPUPercent float64 `json:"cpu_percent"`
	MemUsage   int64   `json:"mem_usage"`
}

// ringSize 保留的采样点数：10 秒一个点，180 个点约 30 分钟。
const ringSize = 180

// cpuSnap 记录上一次累计 CPU 时间，用于算出区间内的百分比。
type cpuSnap struct {
	at    time.Time
	usage int64
}

// Loop 是后台对账循环。
type Loop struct {
	client   *incus.Client
	interval time.Duration
	log      *slog.Logger

	mu        sync.RWMutex
	views     []incus.View
	resources *incus.Resources
	lastOK    time.Time
	lastErr   string
	prevCPU   map[string]cpuSnap
	series    map[string][]Sample
}

// New 建立循环体。
func New(c *incus.Client, interval time.Duration, log *slog.Logger) *Loop {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &Loop{
		client:   c,
		interval: interval,
		log:      log,
		prevCPU:  map[string]cpuSnap{},
		series:   map[string][]Sample{},
	}
}

// Interval 返回采集间隔，健康判定要用它推算容忍窗口。
func (l *Loop) Interval() time.Duration { return l.interval }

// Run 阻塞运行，先立刻取一次，再按间隔轮询。
func (l *Loop) Run(ctx context.Context) {
	l.refresh(ctx)
	t := time.NewTicker(l.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.refresh(ctx)
		}
	}
}

func (l *Loop) refresh(ctx context.Context) {
	res, resErr := l.client.GetResources(ctx)
	insts, err := l.client.ListInstances(ctx)
	if err != nil {
		l.markError(err)
		return
	}

	now := time.Now()
	views := make([]incus.View, 0, len(insts))
	for _, inst := range insts {
		// 停机实例不去取 state：既没有内容，也会白白拖慢一轮对账
		var st *incus.State
		if inst.Status == "Running" {
			if s, sErr := l.client.GetState(ctx, inst.Name); sErr == nil {
				st = s
			} else {
				l.log.Warn("取实例状态失败", "instance", inst.Name, "err", sErr)
			}
		}
		views = append(views, incus.NewView(inst, st, now))
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })

	l.mu.Lock()
	l.views = views
	if resErr == nil {
		l.resources = res
	}
	l.lastOK = now
	l.lastErr = ""
	l.appendSamplesLocked(views, now)
	l.pruneLocked(views)
	l.mu.Unlock()

	if resErr != nil {
		l.log.Warn("取宿主资源失败，容量卡片会退化", "err", resErr)
	}
}

func (l *Loop) markError(err error) {
	l.mu.Lock()
	l.lastErr = err.Error()
	l.mu.Unlock()
	l.log.Error("对账失败", "err", err)
}

// appendSamplesLocked 追加采样点。调用方必须持有写锁。
func (l *Loop) appendSamplesLocked(views []incus.View, now time.Time) {
	for _, v := range views {
		if !v.Running {
			continue
		}
		hostCores := 0
		if l.resources != nil {
			hostCores = l.resources.CPU.Total
		}
		s := Sample{TS: now.Unix(), MemUsage: v.MemUsage}

		cores := float64(v.QuotaCPUNum())
		if cores <= 0 {
			cores = float64(hostCores)
		}
		if cores <= 0 {
			cores = 1
		}
		// 累计 CPU 时间是纳秒级，与上一次采样做差才是区间占用
		if prev, ok := l.prevCPU[v.Name]; ok {
			wall := now.Sub(prev.at).Seconds()
			if wall > 0 {
				delta := v.CPUSeconds - float64(prev.usage)/1e9
				if delta >= 0 {
					pct := delta / (wall * cores) * 100
					if pct > 100 {
						pct = 100
					}
					s.CPUPercent = pct
				}
			}
		}
		l.prevCPU[v.Name] = cpuSnap{at: now, usage: int64(v.CPUSeconds * 1e9)}

		buf := append(l.series[v.Name], s)
		if len(buf) > ringSize {
			buf = buf[len(buf)-ringSize:]
		}
		l.series[v.Name] = buf
	}
}

// pruneLocked 清理已经不存在的实例留下的采样与 CPU 基准。
func (l *Loop) pruneLocked(views []incus.View) {
	alive := make(map[string]bool, len(views))
	for _, v := range views {
		alive[v.Name] = true
	}
	for name := range l.series {
		if !alive[name] {
			delete(l.series, name)
			delete(l.prevCPU, name)
		}
	}
}

// Views 返回最近一次成功的实例快照。
func (l *Loop) Views() []incus.View {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]incus.View, len(l.views))
	copy(out, l.views)
	return out
}

// Resources 返回宿主资源，可能为空（取不到时容量卡片退化为只显示已分配）。
func (l *Loop) Resources() *incus.Resources {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.resources
}

// LastOK 最近一次成功对账的时刻；零值表示从未成功。
func (l *Loop) LastOK() time.Time {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.lastOK
}

// LastErr 最近一次错误信息，成功时为空。
func (l *Loop) LastErr() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.lastErr
}

// Series 返回某实例的采样序列。
func (l *Loop) Series(name string) []Sample {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Sample, len(l.series[name]))
	copy(out, l.series[name])
	return out
}
