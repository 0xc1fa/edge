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

// gpuVisible 决定一条预约该不该出现在某个角色看到的列表里。
//
// 成员只看「已经发生过调度」的：卡真的挂上去过，才对得起「调度日志」这四个字。
// 没挂过的（待批准、已批准、已驳回、已取消）对他既不能操作，
// 也容易被误读成进度 —— 看见「已批准」的人会以为卡已经到手了，而那一刻卡还没动。
//
// 抽成函数而不是内联在循环里，是因为这条边界要能被测试单独钉住：
// 它是一条产品口径，改起来必须有人明确地改一次。
func gpuVisible(b store.GPUBooking, isAdmin bool) bool {
	if isAdmin {
		return true
	}
	return b.AttachedAt != ""
}

// ---------------------------------------------------------------- 列表

// handleGPUList 返回预约列表。
//
// 成员也能看，而且**应当**能看：他要回答的第一个问题是「我想约那天还有没有位子」，
// 看不到别人的占用就只能一遍遍提交、一遍遍被拒。
//
// 但两边看到的不是同一份东西：
//
//	管理员 —— 全部。他要点批准、点挂载，看不到待办就没法干活。
//	成员   —— 只有真正发生过调度的那些（挂上去过的）。
//
// 待批准、已批准、已驳回、已取消，对成员都是噪音：他既批不了也挂不上，
// 看到「已批准」反而会以为卡已经有着落了 —— 而那一刻卡其实还没动。
// 这一页对成员的意义是「调度日志」：哪台机器、什么时候挂的、什么时候摘的。
//
// 裁剪放在服务端而不是前端：前端藏起来的东西，接口照样会给出去。
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
		if !gpuVisible(b, isAdmin) {
			continue
		}
		out = append(out, gpuToView(b, now, !isAdmin))
	}
	if !isAdmin {
		// 待办数只对管理员有意义：成员面前没有待办这一栏
		pending = 0
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

// gpuMountedBeforeBoot 判断这台机器在**本次容器启动之前**就已经带着一张卡。
//
// 这件事决定挂上去的卡要不要重启才看得见。驱动钩子（/dev/nvidia*、CUDA 库）
// 是在容器**启动时**注入的：启动那一刻没有卡，运行时才插进去的设备就有、
// 但容器里没人给它铺好环境，于是 nvidia-smi 看不到，得重启一次让钩子补跑。
// 反过来，只要这次启动本来带着卡，之后的插拔都是真热插 —— 实测同一 PID 下，
// 摘掉设备 /dev/nvidia0 立刻消失，插回去两秒内就认得卡。
//
// 所以判据是「容器启动时有没有卡」，不是「此刻有没有卡」、也不是「runtime 开了没有」：
// runtime 开着却从没重启过的实例，照样看不见卡。
func gpuMountedBeforeBoot(instance string, items []store.GPUBooking, bootAt time.Time) bool {
	if bootAt.IsZero() {
		return false
	}
	// 取这台机器上最早一次真正挂上去的时刻（active 与 ended 才算，
	// 那两种状态下卡确实在实例上待过）
	first := time.Time{}
	for _, b := range items {
		if b.Instance != instance || b.AttachedAt == "" {
			continue
		}
		if b.Status != store.GPUActive && b.Status != store.GPUEnded {
			continue
		}
		t, err := time.Parse(time.RFC3339, b.AttachedAt)
		if err != nil {
			continue
		}
		if first.IsZero() || t.Before(first) {
			first = t
		}
	}
	if first.IsZero() {
		return false // 这台机器从来没挂成功过卡：冷启动，必须重启一次
	}
	return first.Before(bootAt)
}

// gpuWantsRestart 这次挂载之后要不要请管理员重启实例。
//
// 两个信号其实在问同一件事 —— 「这个容器进程有没有带着 GPU 设备起步过」：
//
//	挂载前实例上就已经挂着卡 → 这次生命周期它接触过 GPU，钩子必然在；
//	台账里有早于本次启动的挂载记录 → 那次之后重启过，钩子带着起步。
//
// 第一个信号存在的理由不是冗余：台账记录是会被删除的（清理测试数据、
// 抹掉误填的假记录都是正经操作），记录一没，第二个信号就退化成
// 「这台机器从来没挂过卡」，于是明明能热插的情形也被判成要重启。
// 两个信号各自都不完整，合起来才覆盖得住。
//
// 拿不到容器启动时刻（实例没在跑、或 recon 还没扫到）时保守地说"要"：
// 多问一句只是一次询问，漏掉一次会让租户对着 nvidia-smi 查半天。
func gpuWantsRestart(hadGPUDevice bool, instance string, items []store.GPUBooking, bootAt time.Time) bool {
	if hadGPUDevice {
		return false
	}
	if bootAt.IsZero() {
		return true
	}
	return !gpuMountedBeforeBoot(instance, items, bootAt)
}

// gpuNeedsRestart 取值交给 gpuWantsRestart，这里只负责把数据找齐。
//
// 这里刻意不去自动重启 —— 与前几版的做法一致：重启会打断租户正在跑的东西，
// 这个动作只能由人按下去。宿主脚本 tenant-ctl.sh 会在同一情形下替人重启，
// 所以脚本看起来"总是立即生效"，页面则是把这一步摆到管理员面前。
func (s *Server) gpuNeedsRestart(instance string, hadGPUDevice bool) bool {
	for _, v := range s.cfg.Recon.Views() {
		if v.Name != instance {
			continue
		}
		bootAt, _ := time.Parse(time.RFC3339, v.StartedAt)
		return gpuWantsRestart(hadGPUDevice, instance, s.cfg.GPUS.List(), bootAt)
	}
	return true
}

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
	// 挂载**之前**实例上有没有别的卡 —— 这是判断要不要重启的一半依据，
	// 所以要在这里取，等插上去之后就分不清新卡旧卡了
	hadGPUDevice := false
	for _, d := range inst.Devices {
		if d["type"] == "gpu" {
			hadGPUDevice = true
			break
		}
	}

	ctx := r.Context()
	if err := s.cfg.Incus.PatchConfig(ctx, b.Instance, map[string]string{"nvidia.runtime": "true"}); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "设置 nvidia.runtime 失败: " + err.Error()})
		return
	}
	// 只给 pci，不给 productid/vendorid：Incus 的设备校验明确拒这两者与 pci 同时出现
	// （"Cannot use \"productid\" when \"pci\" is set"）。按地址挂载本身已经是精确到卡的，
	// 厂商与型号只是冗余描述 —— 冗余到会被拒绝的程度，就不该写。
	if err := s.cfg.Incus.AddDevice(ctx, b.Instance, map[string]string{
		"name":    dev,
		"type":    "gpu",
		"gputype": "physical",
		"pci":     pci,
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

	// 要不要重启由这次挂载之前容器是否带卡决定，不是一律要。
	// 冷实例第一次挂卡必须重启；已经带着卡启动过的容器，热插就是真的。
	needsRestart := s.gpuNeedsRestart(b.Instance, hadGPUDevice)
	hint := fmt.Sprintf("已挂到 %s（%s，设备名 %s）。", b.Instance, pci, dev)
	if needsRestart {
		hint += "这台实例的容器还没带着卡启动过，租户侧要看到卡需要重启一次。"
	} else {
		hint += "租户侧这次就能用到卡（容器已经带好了驱动环境），不必重启。"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"booking": gpuToView(updated, time.Now(), false),
		// 设备进了实例定义，不等于容器里立刻看得见。
		// 什么时候必须重启、什么时候不用，判据写在 gpuNeedsRestart 的注释里。
		"needs_restart": needsRestart,
		"hint":          hint,
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
		if err := s.cfg.Incus.RemoveGPUDevice(r.Context(), b.Instance, b.Device); err != nil {
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

// handleGPUDelete 把一条预约从台账里抹掉。
//
// 只给管理员（路由层已收口），而且只抹「卡不在实例上」的那些 ——
// active 的那条必须先摘卡，store.Delete 会挡。
//
// 为什么需要它：没有它，一次验证、一次填错实例名留下的记录
// 会永久挂在台账上，而且页面又不能假装它不存在 ——
// 那样管理员就只能靠停机改文件来清理，而停机改文件本身更难核对。
func (s *Server) handleGPUDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	b, err := s.cfg.GPUS.Delete(id)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"booking": gpuToView(b, time.Now(), false),
		"hint":    "已删除 " + b.Instance + " 的这条记录",
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
			if err := s.cfg.Incus.RemoveGPUDevice(context.Background(), b.Instance, b.Device); err != nil {
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
