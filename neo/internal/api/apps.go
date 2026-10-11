package api

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"neo/internal/catalog"
	"neo/internal/incus"
	"neo/internal/store"
)

// ---------------------------------------------------------------- 目录

// handleCatalog 公开：返回可售规格、GPU 附加项与镜像。
// 各项的 Available 由当前宿主余量实时算出，所以页面几乎每次刷新都可能变。
func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	cap := s.currentCapacity()
	writeJSON(w, http.StatusOK, map[string]any{
		"flavors":    catalog.EvaluateFlavors(s.cfg.Catalog.Flavors, cap),
		"gpu_models": catalog.EvaluateGPUs(s.cfg.Catalog.GPUModels, cap),
		"images":     s.cfg.Catalog.Images,
	})
}

// currentCapacity 汇总当前占用，供可售判断使用。
//
// 已生效的实例要算，**待处理申请也要算**：两个人同时勾选最后一张 4090，
// 第一份还没开工第二份就已经付款的话，管理员只能手动去解释「卡没了」。
// 所以 pending 申请在这里按「预占」计入，宁可售罄提示早一点出现。
func (s *Server) currentCapacity() catalog.Capacity {
	var c catalog.Capacity
	c.GPUAllocatedByModel = map[string]int{}
	for _, v := range s.cfg.Recon.Views() {
		c.CPUAllocated += v.QuotaCPUNum()
		c.MemoryAllocated += v.QuotaMemoryBytes()
		c.DiskAllocated += v.QuotaDiskBytes()
		s.countGPUs(&c, v.GPUs)
	}
	if res := s.cfg.Recon.Resources(); res != nil {
		c.CPUTotal = res.CPU.Total
		c.MemoryTotal = res.Memory.Total
	}
	for _, a := range s.cfg.Apps.List(store.StatusPending) {
		if f, ok := s.cfg.Catalog.Find(a.Flavor); ok {
			c.CPUAllocated += f.VCPU
			c.MemoryAllocated += int64(f.MemoryMiB) * 1024 * 1024
			c.DiskAllocated += int64(f.DiskGB) * 1024 * 1024 * 1024
		}
		if a.GPUModel != "" && a.GPUCount > 0 {
			c.GPUAllocatedByModel[a.GPUModel] += a.GPUCount
		}
	}
	return c
}

// countGPUs 把实例上已挂的卡认回型号。
//
// 认不出来（脚本挂载时没写 productid）时全部记到 DefaultTarget 名下：
// 这一侧算多了只是少卖，算少了才会超卖——同一张卡被许给两个租户，
// 是要管理员出面向人解释的事故。
func (s *Server) countGPUs(c *catalog.Capacity, gpus []incus.GPU) {
	fallback, hasFallback := s.cfg.Catalog.DefaultGPUTarget()
	for _, g := range gpus {
		hay := strings.ToLower(g.Product + " " + g.PCI)
		matched := ""
		for _, m := range s.cfg.Catalog.GPUModels {
			if m.MatchPCI != "" && strings.Contains(hay, strings.ToLower(m.MatchPCI)) {
				matched = m.ID
				break
			}
		}
		if matched == "" && hasFallback {
			matched = fallback.ID
		}
		c.GPUAllocatedByModel[matched]++
	}
}

// ---------------------------------------------------------------- 申请（匿名）

type applyRequest struct {
	Hostname     string `json:"hostname"`
	Flavor       string `json:"flavor"`
	Image        string `json:"image"`
	GPUModel     string `json:"gpu_model"`
	GPUCount     int    `json:"gpu_count"`
	SSHPubKey    string `json:"ssh_public_key"`
	Applicant    string `json:"applicant"`
	Contact      string `json:"contact"`
	Purpose      string `json:"purpose"`
	DurationDays int    `json:"duration_days"`
}

// handleApply 匿名提交开通申请。成功后返回回执号，申请人可凭它自助查进度。
func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	src := clientIP(r)
	// 入口这道闸是防试探的：不许脚本反复调用来枚举「哪些主机名已存在」
	if ok, retry := s.cfg.ApplyFailLimit.Allow(src); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "提交过于频繁，请稍后再试"})
		return
	}

	var req applyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		s.cfg.ApplyFailLimit.Record(src)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式不正确"})
		return
	}

	// GPU 是附加项：它必须和标准实例一起申购，不能单独发一单要卡
	if req.GPUCount > catalog.MaxGPUCards {
		s.cfg.ApplyFailLimit.Record(src)
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("单实例最多配 %d 张显卡", catalog.MaxGPUCards),
		})
		return
	}
	if req.GPUCount < 0 {
		s.cfg.ApplyFailLimit.Record(src)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "显卡数量不合法"})
		return
	}

	// 标准实例是主菜，缺了它整单都不成立
	flavor, okF := s.cfg.Catalog.Find(req.Flavor)
	if !okF {
		s.cfg.ApplyFailLimit.Record(src)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请先选择标准实例规格"})
		return
	}
	// 镜像允许留空，表示「用默认的」。
	// 选镜像是个技术判断，不该丢给第一次来申请的人。
	req.Image = strings.TrimSpace(req.Image)
	if req.Image != "" {
		if _, okI := s.cfg.Catalog.FindImage(req.Image); !okI {
			s.cfg.ApplyFailLimit.Record(src)
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "所选镜像不存在或暂未开放"})
			return
		}
	} else {
		def, ok := s.cfg.Catalog.DefaultImage()
		if !ok {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "当前没有可售镜像，请联系管理员"})
			return
		}
		req.Image = def.ID
	}

	// 可售的判断必须在服务端再算一次：前端传来的可选性只是提示，
	// 真到写库这一步，服务端才有权决定是否收下这笔申请。
	cap := s.currentCapacity()
	if avail := catalog.EvaluateFlavors([]catalog.Flavor{flavor}, cap)[0]; !avail.Available {
		s.cfg.ApplyFailLimit.Record(src)
		writeJSON(w, http.StatusConflict, map[string]string{"error": "该规格当前资源不足：" + avail.Reason})
		return
	}
	// GPU：型号 + 张数一起校验。缺型号有数量、或没型号却有数量，都是非法请求
	gpuReq := strings.TrimSpace(req.GPUModel)
	if req.GPUCount > 0 && gpuReq == "" {
		s.cfg.ApplyFailLimit.Record(src)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请指定显卡型号"})
		return
	}
	if gpuReq != "" {
		model, ok := s.cfg.Catalog.FindGPU(gpuReq)
		if !ok {
			s.cfg.ApplyFailLimit.Record(src)
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "不支持的显卡型号"})
			return
		}
		if avail := catalog.EvaluateGPUs([]catalog.GPUModel{model}, cap)[0]; !avail.Available {
			s.cfg.ApplyFailLimit.Record(src)
			writeJSON(w, http.StatusConflict, map[string]string{"error": "显卡资源不足：" + avail.Reason})
			return
		}
		// 张数不能超过「实例上限」与「该型号剩余库存」二者的小者
		used := cap.GPUAllocatedByModel[model.ID]
		if req.GPUCount < 1 || req.GPUCount > catalog.MaxCardsFor(model, used) {
			s.cfg.ApplyFailLimit.Record(src)
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("该型号当前最多可配 %d 张", catalog.MaxCardsFor(model, used)),
			})
			return
		}
	}

	taken := s.takenHostnames()
	if msg := validateApply(req, taken); msg != "" {
		s.cfg.ApplyFailLimit.Record(src)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}

	// 主机名可以不填 —— 由系统生成。
	// 让第一次来的人在「起个什么名字」上卡住是没必要的交互成本，
	// 真要指定时表单里仍然可以填。
	hostname := strings.TrimSpace(req.Hostname)
	if hostname == "" {
		generated, err := autoHostname(taken)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		hostname = generated
	}

	hostname = strings.TrimSpace(hostname)

	// 单据额度在真正写库前才校验：表单填错（主机名重复、公钥格式不对）
	// 不该消耗掉申请人本小时的单据额度。
	if ok, retry := s.cfg.ApplyLimit.Allow(src); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "本小时提交的单数已达上限，请稍后再试"})
		return
	}

	a, err := s.cfg.Apps.Create(store.Application{
		Hostname:     hostname,
		Flavor:       req.Flavor,
		Image:        req.Image,
		GPUModel:     gpuReq,
		GPUCount:     req.GPUCount,
		LoginUser:    catalog.LoginUser,
		SSHPubKey:    strings.TrimSpace(req.SSHPubKey),
		Applicant:    strings.TrimSpace(req.Applicant),
		Contact:      strings.TrimSpace(req.Contact),
		Purpose:      strings.TrimSpace(req.Purpose),
		DurationDays: req.DurationDays,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "申请未能保存：" + err.Error()})
		return
	}
	// 只有真正落到台账的申请才计入额度 —— 这是保护管理员待办的那道限
	s.cfg.ApplyLimit.Record(src)

	writeJSON(w, http.StatusCreated, map[string]any{
		"application": maskedView(a),
		"hint":        "请留存回执号 " + a.ID + "，用它可在本页面查询处理进度",
	})
}

// handleApplicationGet 凭回执号自助查询，返回脱敏视图。
func (s *Server) handleApplicationGet(w http.ResponseWriter, r *http.Request) {
	a, ok := s.cfg.Apps.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "回执号不存在"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"application": maskedView(a)})
}

// ---------------------------------------------------------------- 申请（管理员）

// handleApplications 管理员查看全部申请，含完整联系方式与公钥（开通时要用）。
func (s *Server) handleApplications(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	writeJSON(w, http.StatusOK, map[string]any{
		"applications": s.cfg.Apps.List(status),
		"pending":      s.cfg.Apps.CountPending(),
	})
}

type decisionRequest struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
}

// handleDecision 管理员批准或驳回。
//
// 注意：第一版只改台账状态并生成建议开通命令，**不自动调 Incus 创建实例**。
// 原因见 README —— socket 目前是只读挂载，且真开通涉及端口与 GPU 分配，
// 在还没有端口池与整卡互斥之前自动执行风险大于收益。
func (s *Server) handleDecision(w http.ResponseWriter, r *http.Request) {
	var req decisionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式不正确"})
		return
	}
	a, err := s.cfg.Apps.Decide(r.PathValue("id"), req.Action, strings.TrimSpace(req.Reason))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"application": a,
		"command":     provisionCommand(a, s.cfg.Catalog),
	})
}

// provisionCommand 生成管理员可直接复制的执行命令，把「批准」落成动作。
// 带卡的申请要额外提醒挂卡 —— GPU 是本機最稀缺的资源，
// 分配错了（比如两张卡分给了两个人）很难向用户解释。
func provisionCommand(a store.Application, c catalog.Catalog) string {
	img, _ := c.FindImage(a.Image)
	f, _ := c.Find(a.Flavor)
	gpuNote := ""
	if a.GPUCount > 0 {
		model := "GPU"
		if m, ok := c.FindGPU(a.GPUModel); ok && m.Title != "" {
			model = m.Title
		}
		gpuNote = fmt.Sprintf(
			" # 随后挂载 %d 张 %s：attach-gpu %s <card-id>，并复核整卡互斥",
			a.GPUCount, model, a.Hostname,
		)
	}
	return fmt.Sprintf(
		"# 回执 %s：%s / %s / %s\ncd /root/edge/vm/host && ./tenant-ctl.sh create %s %s %s%s",
		a.ID, a.Hostname, f.Name, img.Title, a.Hostname, img.Alias, f.Name, gpuNote,
	)
}

// ---------------------------------------------------------------- 校验

var hostnameRe = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,30}[a-z0-9])?$`)
var phoneRe = regexp.MustCompile(`^1[3-9]\d{9}$`)
var mailRe = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

var sshKeyPrefixes = []string{
	"ssh-ed25519 ", "ssh-rsa ", "ecdsa-sha2-nistp256 ",
	"sk-ssh-ed25519@openssh.com ", "sk-ecdsa-sha2-nistp256@openssh.com ",
}

// hostnameAlphabet 与 store 的回执号同理，去掉了 0/O/1/I 这类难辨认字符。
const hostnameAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// autoHostname 生成形如 vm-x7k4p 的主机名，保证不与已占用名字冲突。
//
// 用随机而非自增序号：自增会暴露「你现在有 N 个租户」这条信息，
// 也让删除实例后的编号空洞变得有歧义。
func autoHostname(taken map[string]bool) (string, error) {
	max := big.NewInt(int64(len(hostnameAlphabet)))
	var sb strings.Builder
	for attempt := 0; attempt < 20; attempt++ {
		sb.Reset()
		sb.WriteString("vm-")
		for i := 0; i < 5; i++ {
			v, err := rand.Int(rand.Reader, max)
			if err != nil {
				return "", errors.New("主机名生成失败，请稍后重试")
			}
			sb.WriteByte(hostnameAlphabet[v.Int64()])
		}
		name := sb.String()
		if !taken[name] {
			return name, nil
		}
	}
	return "", errors.New("主机名生成失败，请稍后重试")
}

// takenHostnames 汇总已被占用的主机名：现有实例 + 尚未处理的申请。
// 两者都要查，否则两个申请人可能同时拿到同一个名字。
func (s *Server) takenHostnames() map[string]bool {
	out := map[string]bool{}
	for _, v := range s.cfg.Recon.Views() {
		out[v.Name] = true
	}
	for _, a := range s.cfg.Apps.List(store.StatusPending) {
		out[a.Hostname] = true
	}
	return out
}

// validateApply 返回空串表示通过。
//
// 主机名与 SSH 公钥都是**选填**：不填则由系统在服务端生成/忽略。
// 表单越短，第一次来的人越可能真的提交，而不是中途放弃。
func validateApply(req applyRequest, taken map[string]bool) string {
	host := strings.TrimSpace(req.Hostname)
	if host != "" {
		if !hostnameRe.MatchString(host) {
			return "主机名不合法：需以小写字母开头，仅含小写字母、数字与连字符，长度 2~32"
		}
		if taken[host] {
			return "主机名已被占用，请换一个"
		}
	}

	// 公钥选填：留空表示「用系统下发的随机口令登录 root」。
	// 但一旦填了，就要保证能真的登得进去 —— 半截公钥比没有更糟。
	if key := strings.TrimSpace(req.SSHPubKey); key != "" && !validSSHKey(key) {
		return "SSH 公钥格式不正确：请留空（用随机口令）或粘贴完整公钥（ssh-ed25519 / ssh-rsa / ecdsa 开头）"
	}
	// 长度一律按字符数算，不能按字节：一个汉字是 3 字节，
	// 用 len 的话单个中文字也能绕过长度下限。
	if n := utf8.RuneCountInString(strings.TrimSpace(req.Applicant)); n < 2 || n > 32 {
		return "请填写 2~32 个字符的申请人姓名"
	}
	contact := strings.TrimSpace(req.Contact)
	if !phoneRe.MatchString(contact) && !mailRe.MatchString(contact) {
		return "请填写有效的手机号或邮箱，开通结果将通过它通知"
	}
	if n := utf8.RuneCountInString(strings.TrimSpace(req.Purpose)); n < 4 || n > 500 {
		return "请简单说明用途（4~500 字）"
	}
	if req.DurationDays <= 0 {
		req.DurationDays = 30
	}
	if req.DurationDays > 365 {
		return "单次申请时长最多 365 天"
	}
	return ""
}

func validSSHKey(key string) bool {
	if len(key) < 80 || len(key) > 4096 {
		return false
	}
	for _, p := range sshKeyPrefixes {
		if strings.HasPrefix(key, p) {
			// 前缀之后应当还有一段 base64 正文，避免只粘了个类型名
			return len(strings.TrimSpace(strings.TrimPrefix(key, p))) > 40
		}
	}
	return false
}

// ---------------------------------------------------------------- 脱敏

// maskedView 是给申请人自助查询的视图。
// 回执号含随机段不易枚举，但仍做脱敏：联系方式和公钥这类东西
// 一旦泄露是不可撤回的。
func maskedView(a store.Application) map[string]any {
	return map[string]any{
		"id":             a.ID,
		"hostname":       a.Hostname,
		"flavor":         a.Flavor,
		"image":          a.Image,
		"gpu_model":      a.GPUModel,
		"gpu_count":      a.GPUCount,
		"login_user":     a.LoginUser,
		"applicant":      a.Applicant,
		"contact":        maskContact(a.Contact),
		"ssh_public_key": maskKey(a.SSHPubKey),
		"purpose":        a.Purpose,
		"duration_days":  a.DurationDays,
		"status":         a.Status,
		"reason":         a.Reason,
		"created_at":     a.CreatedAt,
		"updated_at":     a.UpdatedAt,
	}
}

func maskContact(c string) string {
	if c == "" {
		return ""
	}
	if at := strings.Index(c, "@"); at > 0 {
		name := c[:at]
		if len(name) <= 2 {
			return name[:1] + "***" + c[at:]
		}
		return name[:2] + "***" + c[at:]
	}
	if len(c) >= 7 {
		return c[:3] + "****" + c[len(c)-4:]
	}
	if len(c) <= 1 {
		return "*"
	}
	return c[:1] + "***"
}

func maskKey(k string) string {
	fields := strings.Fields(k)
	if len(fields) < 2 {
		return k
	}
	blob := fields[1]
	if len(blob) > 12 {
		blob = blob[:12] + "…"
	}
	return fields[0] + " " + blob + " (已脱敏)"
}
