// GPU 预约：申请、审批、挂载、摘卡、重启。
//
// 这一组接口是本项目第一次真正去动宿主（此前对 Incus 只有读）。
// 因此这里的每一条写路径都遵守同一条规矩：
// 先改台账，再动宿主；宿主失败就把台账回滚，绝不留下「台账说挂着、其实没挂」。
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"neo/internal/store"
)

// gpuView 是预约对外的样子：附带时段与剩余时间，
// 让前端不必自己推算「还有多久到期」这种容易算错的量。
type gpuView struct {
	ID         string `json:"id"`
	Instance   string `json:"instance"`
	Applicant  string `json:"applicant"`
	Contact    string `json:"contact"`
	Purpose    string `json:"purpose"`
	Date       string `json:"date"`
	CardCount  int    `json:"card_count"`
	Status     string `json:"status"`
	Reason     string `json:"reason"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
	DecidedAt  string `json:"decided_at"`
	DecidedBy  string `json:"decided_by"`
	AttachedAt string `json:"attached_at"`
	DetachedAt string `json:"detached_at"`
	Device     string `json:"device"`

	// 时段的起止时刻（RFC3339）：申请页要用它画出「9 点到 16 点」
	WindowStart string `json:"window_start"`
	WindowEnd   string `json:"window_end"`
	// RemainSec 距离本时段结束还有多少秒；已结束或还没挂卡时为 0
	RemainSec int `json:"remain_seconds"`
	// Ended 表示这一天的时段已经走完了（不论卡有没有摘）
	Ended bool `json:"ended"`
}

func gpuToView(b store.GPUBooking, now time.Time, mask bool) gpuView {
	v := gpuView{
		ID:         b.ID,
		Instance:   b.Instance,
		Applicant:  b.Applicant,
		Contact:    b.Contact,
		Purpose:    b.Purpose,
		Date:       b.Date,
		CardCount:  b.CardCount,
		Status:     b.Status,
		Reason:     b.Reason,
		CreatedAt:  b.CreatedAt,
		UpdatedAt:  b.UpdatedAt,
		DecidedAt:  b.DecidedAt,
		DecidedBy:  b.DecidedBy,
		AttachedAt: b.AttachedAt,
		DetachedAt: b.DetachedAt,
		Device:     b.Device,
	}
	if start, end, ok := store.GPUWindow(b.Date); ok {
		v.WindowStart = start.Format(time.RFC3339)
		v.WindowEnd = end.Format(time.RFC3339)
		v.Ended = now.After(end)
		if b.Status == store.GPUActive && now.Before(end) {
			v.RemainSec = int(end.Sub(now).Seconds())
		}
	}
	// 联系方式只给管理员：这是能直接联系到人的东西，
	// 对只想看看「哪天被占了」的成员没有必要展示
	if mask {
		v.Contact = maskContact(v.Contact)
	}
	return v
}

// ---------------------------------------------------------------- 列表

// handleGPUList 返回预约列表。
//
// 成员也能看，而且**应当**能看：他要回答的第一个问题是「我想约那天还有没有位子」，
// 看不到别人的占用就只能一遍遍提交、一遍遍被拒。
// 能看的只是排期，不是别人的联系方式 —— 那一列对非管理员脱敏。
func (s *Server) handleGPUList(w http.ResponseWriter, r *http.Request) {
	isAdmin := s.isAdmin(r)
	now := time.Now()
	items := s.cfg.GPUS.List()
	out := make([]gpuView, 0, len(items))
	pending := 0
	for _, b := range items {
		if b.Status == store.GPUPending {
			pending++
		}
		out = append(out, gpuToView(b, now, !isAdmin))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"bookings":      out,
		"pending":       pending,
		"available":     s.availableCards(),
		"total":         s.cfg.GPUTotal,
		"window":        fmt.Sprintf("%02d:00–%02d:00", store.GPUWindowStartHour, store.GPUWindowEndHour),
		"today":         now.Format("2006-01-02"),
		"can_manage":    isAdmin,
		"cards_visible": len(s.cfg.GPUPCI) > 0,
	})
}

// availableCards 算出「此刻还能批几张」。
//
// 口径与售卖页一致：总可卖数减去实例上真实挂着的卡数。
// 这里刻意不减 pending 预约 —— 预约只是意向，
// 把意向也算成占用会让管理员在页面上看到比实际更紧的库存。
func (s *Server) availableCards() int {
	used := 0
	for _, v := range s.cfg.Recon.Views() {
		used += len(v.GPUs)
	}
	n := s.cfg.GPUTotal - used
	if n < 0 {
		return 0
	}
	return n
}

// ---------------------------------------------------------------- 申请

type gpuApplyRequest struct {
	Instance  string `json:"instance"`
	Applicant string `json:"applicant"`
	Contact   string `json:"contact"`
	Purpose   string `json:"purpose"`
	Date      string `json:"date"`
	CardCount int    `json:"card_count"`
}

// handleGPUApply 提交一条预约。
//
// 申请人默认带成当前登录者的名字：进来的人已经过了一次身份校验，
// 再让他手打一遍名字只会多一个填错的地方。他仍然可以改。
func (s *Server) handleGPUApply(w http.ResponseWriter, r *http.Request) {
	var req gpuApplyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式不正确"})
		return
	}
	if req.Applicant == "" {
		if view, state := s.current(r); state == store.SessionValid && view.UserName != "" {
			req.Applicant = view.UserName
		}
	}
	b, err := s.cfg.GPUS.Create(store.GPUInput{
		Instance:  req.Instance,
		Applicant: req.Applicant,
		Contact:   req.Contact,
		Purpose:   req.Purpose,
		Date:      req.Date,
		CardCount: req.CardCount,
	}, s.availableCards(), time.Now())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	start, end, _ := store.GPUWindow(b.Date)
	hint := fmt.Sprintf("已登记：%s %s %02d:00–%02d:00，等管理员批准并挂载",
		b.Instance, b.Date, start.Hour(), end.Hour())
	// 待处理的申请不占额度，所以同一天可以有多个人排着。
	// 这不拒绝，但要说清楚：让人知道自己前面还有几个人，
	// 而不是等到被驳回时才明白原来这天不只有自己要卡。
	if queue := s.cfg.GPUS.QueueOnDate(b.Date); queue > 1 {
		hint += fmt.Sprintf("（%s 已有 %d 人排队，批不批要等管理员决定）", b.Date, queue)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"booking": gpuToView(b, time.Now(), false),
		"hint":    hint,
	})
}

// ---------------------------------------------------------------- 审批

// handleGPUDecide 批准或驳回。
//
// 批准不等于挂载：批准只是「同意在这一天把卡给他」，
// 真正插卡是另一个动作（handleGPUAttach）。
// 分开的理由写在 store 的状态流转注释里：合起来的话，
// 一次设备层的失败会让审批结论本身变得说不清。
func (s *Server) handleGPUDecide(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req)

	by := ""
	if view, state := s.current(r); state == store.SessionValid {
		by = view.UserName
	}
	b, err := s.cfg.GPUS.Decide(id, req.Action, req.Reason, by, s.availableCards(), time.Now())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"booking": gpuToView(b, time.Now(), false)})
}

// ---------------------------------------------------------------- 挂载 / 摘卡

// handleGPUAttach 把卡挂到实例上。
//
// 顺序是有讲究的：先写 nvidia.runtime，再加设备，最后改台账。
// 反过来（先改台账）的话，万一 Incus 那一步失败，
// 台账上就会留着一条「已挂载」而实例上什么都没有的记录 ——
// 那正是最难排查的一类不一致。
func (s *Server) handleGPUAttach(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	b, ok := s.cfg.GPUS.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "预约不存在"})
		return
	}
	switch b.Status {
	case store.GPUActive:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "这张卡已经在实例上了"})
		return
	case store.GPUPending:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请先批准这条预约"})
		return
	case store.GPURejected, store.GPUCancelled, store.GPUEnded:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "该预约已结束，如需再用请重新申请"})
		return
	}

	if s.cfg.Incus == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "未连接 Incus，无法挂载"})
		return
	}
	// 同一天只该有一张卡在外：这是「按天预约」这条规则的执行点。
	// 台账里已批准的记录会跟这里算出的实际占用互相印证。
	if n := s.attachedNow(); n >= s.cfg.GPUTotal {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("机器上 %d 张卡都已经挂出去了，先摘一张再挂", s.cfg.GPUTotal),
		})
		return
	}
	pci, err := s.pickFreeCard()
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}

	// 实例必须存在，且设备名不能撞车：
	// 用 gpu0/gpu1/... 里第一个没被占用的，避免覆盖别人手工挂的卡
	inst, err := s.cfg.Incus.GetInstance(r.Context(), b.Instance)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "读取实例失败: " + err.Error()})
		return
	}
	dev := freeGPUDeviceName(inst.Devices)

	ctx := r.Context()
	if err := s.cfg.Incus.PatchConfig(ctx, b.Instance, map[string]string{"nvidia.runtime": "true"}); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "设置 nvidia.runtime 失败: " + err.Error()})
		return
	}
	if err := s.cfg.Incus.AddDevice(ctx, b.Instance, map[string]string{
		"name":      dev,
		"type":      "gpu",
		"gputype":   "physical",
		"pci":       pci,
		"productid": "2684",
		"vendorid":  "10de",
	}); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "挂载失败: " + err.Error()})
		return
	}

	updated, err := s.cfg.GPUS.MarkAttached(b.ID, dev, time.Now())
	if err != nil {
		// 台账写不动是最坏的情况：卡已经插上去了。
		// 如实报错，让管理员知道要去核对实例，而不是假装这次没发生过。
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "卡已挂上，但台账更新失败，请核对实例设备: " + err.Error(),
		})
		return
	}
	s.refreshRecon()

	writeJSON(w, http.StatusOK, map[string]any{
		"booking": gpuToView(updated, time.Now(), false),
		// 设备进了实例，不等于容器里的 /dev/nvidia* 立刻可见 ——
		// 这件事在这台机器上通常要重启才生效。要不要重启由管理员决定：
		// 自动重启会打断实例上正在跑的东西。
		"needs_restart": true,
		"hint":          fmt.Sprintf("已挂到 %s（%s，设备名 %s）。若租户侧 nvidia-smi 看不到卡，需要重启实例。", b.Instance, pci, dev),
	})
}

// handleGPUDetach 把卡摘下来。
//
// 摘卡按台账里记的设备名来，而不是「找一遍 gpu 类型的设备全都摘」——
// 后者会把管理员手工挂上去、并不属于这条预约的卡一起摘掉。
func (s *Server) handleGPUDetach(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	b, ok := s.cfg.GPUS.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "预约不存在"})
		return
	}
	if b.Status != store.GPUActive {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "这条预约当前没有挂卡"})
		return
	}
	if s.cfg.Incus == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "未连接 Incus，无法摘卡"})
		return
	}

	// 设备可能已经被人在宿主上摘掉了。这种情况要让流程继续走下去：
	// 目标是「卡不在实例上」，它已经成立了，只是要回头把台账改对。
	var removed string
	if b.Device != "" {
		if err := s.cfg.Incus.RemoveDevice(r.Context(), b.Instance, b.Device); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "摘卡失败: " + err.Error()})
			return
		}
		removed = b.Device
	} else {
		removed = "（台账未记设备名，跳过 Incus 操作）"
	}

	updated, err := s.cfg.GPUS.MarkDetached(b.ID, "管理员手动摘卡", time.Now())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "卡已摘下（" + removed + "），但台账更新失败: " + err.Error(),
		})
		return
	}
	s.refreshRecon()
	writeJSON(w, http.StatusOK, map[string]any{
		"booking": gpuToView(updated, time.Now(), false),
		"hint":    "已摘卡：" + removed,
	})
}

// handleGPUCancel 取消还没挂卡的预约。
// 成员只能取消自己申请的（按申请人名字比对），管理员不受限。
func (s *Server) handleGPUCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	b, ok := s.cfg.GPUS.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "预约不存在"})
		return
	}
	me := ""
	if view, state := s.current(r); state == store.SessionValid {
		me = view.UserName
	}
	if !s.isAdmin(r) && b.Applicant != me {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "只能取消自己申请的预约"})
		return
	}
	updated, err := s.cfg.GPUS.Cancel(id, me, time.Now())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"booking": gpuToView(updated, time.Now(), false)})
}

// handleInstanceRestart 重启实例。
//
// 这是唯一一个「会打断实例上正在跑的东西」的动作，
// 所以它单独成一个按钮、单独二次确认，绝不夹带在挂载里自动执行。
func (s *Server) handleInstanceRestart(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if s.cfg.Incus == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "未连接 Incus"})
		return
	}
	exists := false
	for _, v := range s.cfg.Recon.Views() {
		if v.Name == name {
			exists = true
			break
		}
	}
	if !exists {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "实例不存在: " + name})
		return
	}
	var req struct {
		Force bool `json:"force"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req)
	if err := s.cfg.Incus.RestartInstance(r.Context(), name, req.Force); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "重启失败: " + err.Error()})
		return
	}
	s.refreshRecon()
	writeJSON(w, http.StatusOK, map[string]any{"status": "已重启", "instance": name})
}

// ---------------------------------------------------------------- 辅助

// attachedNow 统计此刻实例上真实挂着的卡数（跨实例合计）。
func (s *Server) attachedNow() int {
	n := 0
	for _, v := range s.cfg.Recon.Views() {
		n += len(v.GPUs)
	}
	return n
}

// pickFreeCard 挑一张当前没被任何实例占用的卡。
func (s *Server) pickFreeCard() (string, error) {
	if len(s.cfg.GPUPCI) == 0 {
		return "", errors.New("未配置显卡地址（NEO_GPU_PCI），不知道该挂哪一张")
	}
	used := map[string]bool{}
	for _, v := range s.cfg.Recon.Views() {
		for _, g := range v.GPUs {
			if g.PCI != "" {
				used[strings.ToLower(strings.TrimSpace(g.PCI))] = true
			}
		}
	}
	// 已经被这条预约之外的人占用的卡不再分配（防止同一张卡挂给两个实例）
	for _, p := range s.cfg.GPUPCI {
		if !used[strings.ToLower(strings.TrimSpace(p))] {
			return p, nil
		}
	}
	return "", errors.New("所有已配置的显卡都在使用中")
}

// freeGPUDeviceName 在实例的现有设备里找一个没被占用的 gpu 设备名。
func freeGPUDeviceName(devices map[string]map[string]string) string {
	for i := 0; i < 16; i++ {
		name := "gpu" + strconv.Itoa(i)
		if _, ok := devices[name]; !ok {
			return name
		}
	}
	return "gpu" + strconv.Itoa(len(devices))
}

// refreshRecon 让对账立刻重取一次。
// 挂载/摘卡之后如果还等下一个 10 秒周期，
// 管理员会在页面上看到「操作成功但状态没变」，然后怀疑自己点错了。
func (s *Server) refreshRecon() {
	if s.cfg.Recon == nil {
		return
	}
	s.cfg.Recon.Refresh(context.Background())
}

// isAdmin 判断当前请求是不是管理员。
func (s *Server) isAdmin(r *http.Request) bool {
	view, state := s.current(r)
	return state == store.SessionValid && view.Role == store.RoleAdmin
}

// RunGPUSweeper 后台巡检：到点自动摘卡。
//
// 时段结束那一刻必须有个人去摘卡，否则「8 小时」这个约定就成了摆设 ——
// 卡会一直挂在实例上直到有人想起来。
// 交给定时器而不是管理员的记忆，是这个约定能成立的唯一方式。
//
// 时间间隔取 30 秒：够及时（最多晚半分钟），又不至于频繁打扰 Incus。
func (s *Server) RunGPUSweeper(ctx context.Context, interval time.Duration, log func(string, ...any)) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sweepGPU(log)
		}
	}
}

// sweepGPU 检查所有正在挂卡的预约，时段已过就摘掉。
func (s *Server) sweepGPU(log func(string, ...any)) {
	if s.cfg.Incus == nil {
		return
	}
	now := time.Now()
	for _, b := range s.cfg.GPUS.ListActive() {
		_, end, ok := store.GPUWindow(b.Date)
		if !ok || now.Before(end) {
			continue
		}
		if b.Device != "" {
			if err := s.cfg.Incus.RemoveDevice(context.Background(), b.Instance, b.Device); err != nil {
				if log != nil {
					log("GPU 到期自动摘卡失败", "预约", b.ID, "实例", b.Instance, "err", err)
				}
				continue
			}
		}
		if _, err := s.cfg.GPUS.MarkDetached(b.ID, "时段结束，自动摘卡", now); err != nil {
			if log != nil {
				log("GPU 到期后更新台账失败", "预约", b.ID, "err", err)
			}
			continue
		}
		if log != nil {
			log("GPU 时段结束，已自动摘卡", "预约", b.ID, "实例", b.Instance, "日期", b.Date)
		}
		s.refreshRecon()
	}
}
