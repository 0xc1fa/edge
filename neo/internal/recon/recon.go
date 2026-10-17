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

// Refresh 立刻重取一次快照。
//
// 存在理由是挂载与摘卡：那两个动作改的是宿主上的事实，
// 而对账默认是每 10 秒一轮。不主动催一次的话，
// 管理员会看到「操作成功」但页面上设备还没出现，然后怀疑自己点错了。
func (l *Loop) Refresh(ctx context.Context) { l.refresh(ctx) }

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
		v := incus.NewView(inst, st, now)
		// 当前 CPU 占用。与 series 用的是同一套算法（累计时间差 / 墙钟差 / 核数），
		// 但多存一份在 View 上：列表页要每台都显示「现在多少」，
		// 而 series 只为选中的那一台保留。
		if v.Running {
			v.CPUPercent = l.cpuPercentLocked(v, now)
		}
		views = append(views, v)
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

// cpuPercentLocked 用两次采样的差算出瞬时 CPU 占用。
//
// 单看累计值是算不出来的：cpu_seconds 单调递增，
// 「现在用了多少」必须靠「这段时间涨了多少」除以「这段时间过了多久」。
// 所以这个函数是 recon 的职责 —— 只有它持有上一次的采样点。
//
// 调用方必须持有写锁（它会更新 prevCPU 基准）。
func (l *Loop) cpuPercentLocked(v incus.View, now time.Time) float64 {
	hostCores := 0
	if l.resources != nil {
		hostCores = l.resources.CPU.Total
	}
	cores := float64(v.QuotaCPUNum())
	if cores <= 0 {
		cores = float64(hostCores)
	}
	if cores <= 0 {
		cores = 1
	}
	prev, ok := l.prevCPU[v.Name]
	if !ok {
		// 第一次见：先记基准，这一轮没有百分比可报
		l.prevCPU[v.Name] = cpuSnap{at: now, usage: int64(v.CPUSeconds * 1e9)}
		return 0
	}
	wall := now.Sub(prev.at).Seconds()
	if wall <= 0 {
		return 0
	}
	delta := v.CPUSeconds - float64(prev.usage)/1e9
	if delta < 0 {
		delta = 0
	}
	pct := delta / (wall * cores) * 100
	if pct > 100 {
		pct = 100
	}
	l.prevCPU[v.Name] = cpuSnap{at: now, usage: int64(v.CPUSeconds * 1e9)}
	return pct
}

// appendSamplesLocked 追加采样点。调用方必须持有写锁。
func (l *Loop) appendSamplesLocked(views []incus.View, now time.Time) {
	for _, v := range views {
		if !v.Running {
			continue
		}
		s := Sample{TS: now.Unix(), MemUsage: v.MemUsage}

		// 百分比复用同一段算法：列表页与曲线图必须给出同一个数，
		// 否则「卡片上说 40%、折线图上却看不出来」这种矛盾没法解释。
		// 注意它会顺带更新 prevCPU 基准 —— 所以 refresh 里的列表百分比
		// 必须先算好，否则这里会把基准推到与列表同一次采样上。
		s.CPUPercent = l.cpuPercentLocked(v, now)

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
