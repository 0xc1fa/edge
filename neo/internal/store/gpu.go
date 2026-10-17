// GPU 预约台账。
//
// 与开通申请（Application）是两件事，不要混在一起：
// 开通申请是「给我一台新机器」，预约是「给我这台机器上那张卡，用一天里的某 8 小时」。
// 前者要人工开通，后者是热挂载 —— 动作的性质不同，留痕的要求也不同。
package store

import (
	"crypto/rand"
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

// GPU 预约状态流转：
//
//	pending ──批准──► approved ──挂载──► active ──到期/摘卡──► ended
//	   │                  │                  │
//	   └──驳回──► rejected  └──取消──► cancelled
//
// 把「批准」和「挂载」分成两步是有意的：批准是同意，挂载是执行。
// 合起来做的话，批准时卡正被别人占着就会失败，而失败会让审批结论本身变得含糊。
const (
	GPUPending   = "pending"
	GPUApproved  = "approved"
	GPURejected  = "rejected"
	GPUActive    = "active"
	GPUEnded     = "ended"
	GPUCancelled = "cancelled"
)

// 时段是固定的：每天 09:00 到 16:00。
//
// 注意这是 7 小时而不是 8 小时 —— 需求里同时写了「9:00 到 16:00」和「8 小时」，
// 两者对不上，这里按明确的时刻实现（时刻是可执行的，小时数是推算的）。
// 要凑满 8 小时，把下面两个常量里的一个改掉即可（09:00–17:00 或 08:00–16:00）。
//
// 不让它可配置，是因为它来自一种约定而不是技术约束 ——
// 白天有人值守的时候才把卡交出去，出事能立刻找到人。
// 写成常量而不是配置，是为了让这个约定在代码里看得见。
const (
	GPUWindowStartHour = 9
	GPUWindowEndHour   = 16
)

const gpuFile = "gpu-bookings.json"

// GPUBooking 是一条 GPU 预约。
type GPUBooking struct {
	ID string `json:"id"` // 回执号 GU-20261017-XXXX
	// Instance 是目标实例名。预约一定挂在某台具体机器上 ——
	// 「给我一张卡」这种诉求没有落点，挂载也就无从执行。
	Instance   string `json:"instance"`
	Applicant  string `json:"applicant"`
	Contact    string `json:"contact"`
	Purpose    string `json:"purpose"`
	Date       string `json:"date"`       // YYYY-MM-DD
	CardCount  int    `json:"card_count"` // 张数，默认 1
	Status     string `json:"status"`
	Reason     string `json:"reason"` // 驳回意见，或摘卡时的说明
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
	DecidedAt  string `json:"decided_at"`
	DecidedBy  string `json:"decided_by"`
	AttachedAt string `json:"attached_at"` // 实际挂上去的时刻
	DetachedAt string `json:"detached_at"` // 实际摘下来的时刻
	// Device 是挂载时写进实例的设备名（如 gpu0）。
	// 摘卡必须按这个名字摘，不能靠「重新找一遍 gpu 类型的设备」——
	// 那会把别人手工挂的卡一起摘掉。
	Device string `json:"device"`
}

// GPUInput 是提交预约时的输入。Date 留空表示今天。
type GPUInput struct {
	Instance  string
	Applicant string
	Contact   string
	Purpose   string
	Date      string
	CardCount int
}

// GPUStore 是预约台账。
type GPUStore struct {
	mu    sync.Mutex
	path  string
	items []GPUBooking
}

// NewGPUStore 建立台账并载入已有数据。
func NewGPUStore(dir string) (*GPUStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建数据目录失败: %w", err)
	}
	s := &GPUStore{path: filepath.Join(dir, gpuFile)}
	if raw, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(raw, &s.items); err != nil {
			return nil, fmt.Errorf("GPU 预约台账解析失败，请人工核对 %s: %w", s.path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取 GPU 预约台账失败: %w", err)
	}
	return s, nil
}

// Create 登记一条预约。
//
// available 是这一天还能批的卡数（由调用方按库存算好传进来，
// 台账不认识 catalog —— 库存口径必须只有一个来源）。
// 冲突在登记时就挡掉，而不是等到批准时才说不行：
// 让人走完一遍流程再被拒绝，比一开始就告诉他这天满了对谁都没好处。
func (s *GPUStore) Create(in GPUInput, available int, now time.Time) (GPUBooking, error) {
	date, err := normalizeDate(in.Date, now)
	if err != nil {
		return GPUBooking{}, err
	}
	instance := strings.TrimSpace(in.Instance)
	if instance == "" {
		return GPUBooking{}, errors.New("请选择要挂载的实例")
	}
	if strings.TrimSpace(in.Applicant) == "" {
		return GPUBooking{}, errors.New("请填写申请人")
	}
	if len([]rune(strings.TrimSpace(in.Purpose))) > 200 {
		return GPUBooking{}, errors.New("用途不超过 200 字")
	}
	cards := in.CardCount
	if cards <= 0 {
		cards = 1
	}
	if cards > MaxGPUCardsPerBooking {
		return GPUBooking{}, fmt.Errorf("单次预约最多 %d 张", MaxGPUCardsPerBooking)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if used := s.usedOnDateLocked(date, ""); used+cards > available {
		return GPUBooking{}, fmt.Errorf("%s 的卡已被约满（可用 %d，已约 %d）", date, available, used)
	}

	b := GPUBooking{
		ID:        newBookingID(now),
		Instance:  instance,
		Applicant: strings.TrimSpace(in.Applicant),
		Contact:   strings.TrimSpace(in.Contact),
		Purpose:   strings.TrimSpace(in.Purpose),
		Date:      date,
		CardCount: cards,
		Status:    GPUPending,
		CreatedAt: now.Format(time.RFC3339),
		UpdatedAt: now.Format(time.RFC3339),
	}
	s.items = append(s.items, b)
	if err := s.saveLocked(); err != nil {
		s.items = s.items[:len(s.items)-1]
		return GPUBooking{}, err
	}
	return b, nil
}

// MaxGPUCardsPerBooking 单次预约的张数上限。
// 与 catalog.MaxGPUCards 同值：一次预约不该把整台机器的卡全包了。
const MaxGPUCardsPerBooking = 2

// usedOnDateLocked 统计某天已被占用（已批准或正在使用）的卡数。
// exclude 用于把某条记录排除在外（改判时它自己不该跟自己冲突）。
func (s *GPUStore) usedOnDateLocked(date, exclude string) int {
	n := 0
	for _, b := range s.items {
		if b.Date != date || b.ID == exclude {
			continue
		}
		if b.Status == GPUApproved || b.Status == GPUActive {
			n += b.CardCount
		}
	}
	return n
}

// Get 按回执号取一条预约。
func (s *GPUStore) Get(id string) (GPUBooking, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.items {
		if b.ID == id {
			return b, true
		}
	}
	return GPUBooking{}, false
}

// List 返回全部预约，按「日期倒序、同日按创建时间倒序」。
//
// 排序按日期而不是提交时间：管理员看这张表要回答的是
// 「今天谁在用」「明天谁约了」，提交早晚对这个问题没有意义。
func (s *GPUStore) List() []GPUBooking {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]GPUBooking, len(s.items))
	copy(out, s.items)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date > out[j].Date
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out
}

// ListActive 返回正在挂着卡的预约，供到期巡检用。
func (s *GPUStore) ListActive() []GPUBooking {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]GPUBooking, 0, len(s.items))
	for _, b := range s.items {
		if b.Status == GPUActive {
			out = append(out, b)
		}
	}
	return out
}

// Decide 批准或驳回。只接受从 pending 出发，重复处理会报错 ——
// 同一条申请被处理两次通常意味着两个管理员同时点了按钮。
func (s *GPUStore) Decide(id, action, reason, by string, available int, now time.Time) (GPUBooking, error) {
	want := ""
	switch action {
	case "approve":
		want = GPUApproved
	case "reject":
		want = GPURejected
	default:
		return GPUBooking{}, errors.New("处理动作只支持 approve / reject")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		b := s.items[i]
		if b.ID != id {
			continue
		}
		if b.Status != GPUPending {
			return GPUBooking{}, errors.New("该预约已被处理")
		}
		// 批准要重新查一次冲突：从提交到批准之间，
		// 同一天的卡可能已经被另一条预约占走了
		if want == GPUApproved {
			if used := s.usedOnDateLocked(b.Date, b.ID); used+b.CardCount > available {
				return GPUBooking{}, fmt.Errorf("%s 的卡已被约满，无法批准", b.Date)
			}
		}
		prev := b
		s.items[i].Status = want
		s.items[i].Reason = strings.TrimSpace(reason)
		s.items[i].DecidedAt = now.Format(time.RFC3339)
		s.items[i].DecidedBy = by
		s.items[i].UpdatedAt = now.Format(time.RFC3339)
		if err := s.saveLocked(); err != nil {
			s.items[i] = prev
			return GPUBooking{}, err
		}
		return s.items[i], nil
	}
	return GPUBooking{}, errors.New("预约不存在")
}

// Cancel 取消预约。只有还没挂卡的能取消：
// 已经在用的要提前结束，走摘卡那条路，让台账上留下完整的起止。
func (s *GPUStore) Cancel(id, by string, now time.Time) (GPUBooking, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		b := s.items[i]
		if b.ID != id {
			continue
		}
		switch b.Status {
		case GPUPending, GPUApproved:
		default:
			return GPUBooking{}, errors.New("只有在挂卡之前才能取消；已在使用的请先摘卡")
		}
		prev := b
		s.items[i].Status = GPUCancelled
		s.items[i].DecidedAt = now.Format(time.RFC3339)
		s.items[i].DecidedBy = by
		s.items[i].UpdatedAt = now.Format(time.RFC3339)
		if err := s.saveLocked(); err != nil {
			s.items[i] = prev
			return GPUBooking{}, err
		}
		return s.items[i], nil
	}
	return GPUBooking{}, errors.New("预约不存在")
}

// MarkAttached 记录「卡已经挂上去了」。device 是写进实例的设备名，摘卡时要用。
func (s *GPUStore) MarkAttached(id, device string, now time.Time) (GPUBooking, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		b := s.items[i]
		if b.ID != id {
			continue
		}
		if b.Status != GPUApproved && b.Status != GPUActive {
			return GPUBooking{}, errors.New("只有已批准的预约才能挂载")
		}
		prev := b
		s.items[i].Status = GPUActive
		s.items[i].Device = device
		s.items[i].AttachedAt = now.Format(time.RFC3339)
		s.items[i].UpdatedAt = now.Format(time.RFC3339)
		if err := s.saveLocked(); err != nil {
			s.items[i] = prev
			return GPUBooking{}, err
		}
		return s.items[i], nil
	}
	return GPUBooking{}, errors.New("预约不存在")
}

// MarkDetached 记录「卡已经摘下来了」。
// reason 区分两种情况：到期自动摘、管理员手动摘 —— 台账上要能看出区别。
func (s *GPUStore) MarkDetached(id, reason string, now time.Time) (GPUBooking, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		b := s.items[i]
		if b.ID != id {
			continue
		}
		if b.Status != GPUActive {
			return GPUBooking{}, errors.New("该预约当前没有挂卡")
		}
		prev := b
		s.items[i].Status = GPUEnded
		s.items[i].DetachedAt = now.Format(time.RFC3339)
		s.items[i].UpdatedAt = now.Format(time.RFC3339)
		if reason != "" {
			s.items[i].Reason = reason
		}
		if err := s.saveLocked(); err != nil {
			s.items[i] = prev
			return GPUBooking{}, err
		}
		return s.items[i], nil
	}
	return GPUBooking{}, errors.New("预约不存在")
}

// UsedOnDate 统计某天已占用的卡数，供接口回答「这天还有没有位子」。
func (s *GPUStore) UsedOnDate(date string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usedOnDateLocked(date, "")
}

// QueueOnDate 统计某天在排队的条数（待处理 + 已批准）。
//
// 单独给一个方法而不是让调用方自己过滤，是因为「排队」这个口径
// 要在提交时给人一句提示：这一天已经有人排着了，你不是第一个。
// 待处理的申请不占额度（批准才占），所以这不是拒绝，只是知情 ——
// 让人在等的时候知道还有别人在等，而不是以为自己是唯一的一个。
func (s *GPUStore) QueueOnDate(date string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, b := range s.items {
		if b.Date == date && (b.Status == GPUPending || b.Status == GPUApproved) {
			n++
		}
	}
	return n
}

func (s *GPUStore) saveLocked() error {
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

// normalizeDate 把日期串规整成 YYYY-MM-DD，留空表示今天。
//
// 「今天」按服务所在时区算（容器里是 Asia/Shanghai），
// 与页面上管理员看到的日期必须是同一个 —— 否则会出现
// 「我约的是今天，台账上却写着昨天」这种说不清的偏差。
func normalizeDate(v string, now time.Time) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return now.Format("2006-01-02"), nil
	}
	t, err := time.ParseInLocation("2006-01-02", v, time.Local)
	if err != nil {
		return "", errors.New("日期格式应为 YYYY-MM-DD")
	}
	// 不约过去的日子：那是补记，不是预约。
	// 允许今天 —— 上午约下午用是正常诉求。
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	if t.Before(today) {
		return "", errors.New("不能预约过去的日期")
	}
	if t.After(today.AddDate(0, 0, 30)) {
		return "", errors.New("最多预约 30 天内的日期")
	}
	return t.Format("2006-01-02"), nil
}

// GPUWindow 算出某一天的可用时段的起止时刻。
// 返回的 ok 为 false 表示日期串不合法（时段也就无从谈起）。
func GPUWindow(date string) (start, end time.Time, ok bool) {
	t, err := time.ParseInLocation("2006-01-02", date, time.Local)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	start = time.Date(t.Year(), t.Month(), t.Day(), GPUWindowStartHour, 0, 0, 0, time.Local)
	end = time.Date(t.Year(), t.Month(), t.Day(), GPUWindowEndHour, 0, 0, 0, time.Local)
	return start, end, true
}

// newBookingID 生成形如 GU-20261017-7XK4 的回执号。
// 与开通申请共用同一套字母表（receiptAlphabet）：回执号是要靠嘴和键盘传递的，
// 两个地方用两套易混字符，只会让人在电话里多问一遍。
func newBookingID(t time.Time) string {
	var sb strings.Builder
	sb.WriteString("GU-")
	sb.WriteString(t.Format("20060102"))
	sb.WriteByte('-')
	n := big.NewInt(int64(len(receiptAlphabet)))
	for i := 0; i < 4; i++ {
		v, err := rand.Int(rand.Reader, n)
		if err != nil {
			sb.WriteString(strings.ToUpper(fmt.Sprintf("%x", t.Nanosecond()+i))[:1])
			continue
		}
		sb.WriteByte(receiptAlphabet[v.Int64()])
	}
	return sb.String()
}
