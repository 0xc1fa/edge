// Package limit 提供按来源 IP 的简易滑动窗口限流。
//
// 只做一件事：阻止同一个来源在短时间内无限次触发同一个动作。
// 控制台部署形态下所有请求都可能来自同一个反代地址，
// 此时它退化为全局限流 —— 对单机场景反而是想要的行为。
package limit

import (
	"sync"
	"time"
)

// Limiter 是滑动窗口计数器。
type Limiter struct {
	limit int
	win   time.Duration

	mu   sync.Mutex
	fail map[string][]time.Time
}

// New 建立限流器，limit 为窗口内允许的次数。参数非正时用默认值兜底。
func New(limit int, win time.Duration) *Limiter {
	if limit <= 0 {
		limit = 10
	}
	if win <= 0 {
		win = time.Minute
	}
	return &Limiter{limit: limit, win: win, fail: map[string][]time.Time{}}
}

// Allow 判断来源是否还能操作，并给出建议的重试等待时间。
func (l *Limiter) Allow(src string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	kept := make([]time.Time, 0, len(l.fail[src]))
	for _, t := range l.fail[src] {
		if now.Sub(t) < l.win {
			kept = append(kept, t)
		}
	}
	l.fail[src] = kept
	if len(kept) >= l.limit {
		return false, l.win - now.Sub(kept[0])
	}
	return true, 0
}

// Record 记录一次动作。
func (l *Limiter) Record(src string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fail[src] = append(l.fail[src], time.Now())
}

// Reset 清空来源的记录，通常在成功后调用。
func (l *Limiter) Reset(src string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fail, src)
}
