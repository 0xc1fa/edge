package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"neo/internal/incus"
	"neo/internal/store"
	"neo/internal/wsutil"
)

// 本文件是「租户对自己这台机器能做的事」：网页终端、改登录口令、开关机与重启。
//
// 共同的前提是**归属**：这些动作都会动到一台具体的实例，所以必须先回答
// 「这台机器是不是这个人的」。判据与「成员只能取消自己申请的预约」一致 ——
// 按申请人名字比对（store.Cancel 用的就是这个口径），管理员不受限。
//
// 为什么不让所有登录成员操作所有实例：这套控制台没有做多租户隔离，
// 成员能看到全部实例的监控数据（那是「平台在跑什么」的可见性需求）。
// 但「看」和「动」是两件事 —— 改掉别人的 root 口令、重启别人正在跑任务的机器，
// 这种能力不该随着「能看见」一起发放。
//
// 三个端点都不写台账：它们是基础设施操作，不是业务单据。

// instanceOwner 返回这台实例登记在谁名下，空串表示查不到归属。
//
// 两个来源，优先级从高到低：
//
//	归属台账（管理员在页面上分配的）—— 显式、最新，覆盖一切；
//	申请台账（开通时填的申请人）     —— 自然形成，但只覆盖走控制台申请的实例。
//
// 为什么需要前者：宿主上早有手工建的实例，申请台账里没有它们，
// 于是租户侧的终端/口令/电源对谁都是灰的。分配一次就把它们纳进体系。
//
// 刻意**不**用 GPU 预约台账当来源：那些记录会被清理（删除接口就是为清
// 测试数据加的），清理完归属就凭空消失，功能变成「昨天还能用、今天全灰」。
func (s *Server) instanceOwner(instance string) string {
	if s.cfg.Owners != nil {
		if rec, ok := s.cfg.Owners.Get(instance); ok && rec.Owner != "" {
			return rec.Owner
		}
	}
	for _, a := range s.cfg.Apps.List("") {
		if a.Hostname == instance && a.Applicant != "" {
			return a.Applicant
		}
	}
	return ""
}

// canControl 是归属判定的纯函数：拆出来是为了能单独测。
//
// 三条边界各自对应一种真实情况：
//
//	管理员            —— 不受限，他本来就要替所有人管这些机器；
//	申请人本人        —— 台账里这台机器登记在谁名下，谁就能动；
//	没登记属主的实例   —— 谁都不能动（除了管理员）。系统不知道它归谁，
//	                      「猜一个放行」比拒绝危险得多。
func canControl(isAdmin bool, user, owner string) bool {
	if isAdmin {
		return true
	}
	if user == "" || owner == "" {
		return false
	}
	return user == owner
}

// canControlInstance 判断当前请求者能不能动这台实例。
func (s *Server) canControlInstance(r *http.Request, instance string) bool {
	if s.isAdmin(r) {
		return true
	}
	view, state := s.current(r)
	if state != store.SessionValid {
		return false
	}
	return canControl(false, view.UserName, s.instanceOwner(instance))
}

// instanceExists 实例在不在。用 recon 的视图而不是再问一次 Incus：
// 那里面已经有了，再问一遍只是多一次可能失败的往返。
func (s *Server) instanceExists(instance string) bool {
	for _, v := range s.cfg.Recon.Views() {
		if v.Name == instance {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- 网页终端

// handleInstanceShell 把一条 WebSocket 升级成容器内的交互式 shell。
//
// 为什么不直接转发 SSH：租户实例的 22 端口虽然在宿主上听着，但那条路
// 需要租户手里有口令、还要再实现一套 SSH 客户端；而 Incus 已经能
// 在容器里起一个带 pty 的 shell，走它意味着**不需要口令也能进** ——
// 登录态早已由控制台的会话 cookie 证明过，再问一次口令没有意义。
//
// 代价是依赖 Incus 的 exec 通道：它不可用时网页终端打不开，而 SSH 照旧可用。
// 两条路是互补的，不是谁替代谁。
func (s *Server) handleInstanceShell(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.instanceExists(name) {
		http.Error(w, "实例不存在", http.StatusNotFound)
		return
	}
	if !s.canControlInstance(r, name) {
		http.Error(w, "只能操作自己名下的实例", http.StatusForbidden)
		return
	}
	if s.cfg.Incus == nil {
		http.Error(w, "未连接 Incus，无法打开终端", http.StatusServiceUnavailable)
		return
	}

	// 权限放过之后再升级：升级之后这条连接上跑的就是裸 WebSocket，
	// 再也没有机会用 HTTP 状态码回绝对方。
	conn, err := wsutil.Upgrade(w, r)
	if err != nil {
		return // 升级失败时 wsutil 已经把连接关了
	}
	defer conn.Close()

	session, err := s.cfg.Incus.Exec(r.Context(), name, incus.ExecRequest{
		Command:     []string{"/bin/sh"},
		Interactive: true,
		Env:         map[string]string{"TERM": "xterm-256color"},
	})
	if err != nil {
		// 这一行必须有：exec 失败时连接随即便会被关掉，
		// 而「关闭时客户端还有未读数据」会让内核回一个 RST ——
		// 那个 RST 会连我们正要发出去的错误帧一起吞掉，
		// 使用者看到的只是「终端一闪就没」。
		log.Printf("打开 %s 的网页终端失败: %v", name, err)
		_ = conn.Write(wsutil.OpText, []byte("\r\n打开终端失败: "+err.Error()+"\r\n"))
		return
	}
	defer session.Close()

	// 欢迎词先说清楚这是什么、以及怎么出去。没有它，第一次进来的人
	// 会对着一个静默的光标不知所措，或者更糟 —— 直接关掉浏览器标签，
	// 那会让容器里的 shell 变成没人管的孤儿。
	_ = conn.Write(wsutil.OpText, []byte(
		"\r\n\x1b[32m"+name+" 网页终端\x1b[0m（root，退出请键入 exit 或 Ctrl-D）\r\n\r\n"))

	done := make(chan struct{}, 2)
	// 容器 → 浏览器
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 32*1024)
		for {
			n, err := session.Read(buf)
			if n > 0 {
				if werr := conn.Write(wsutil.OpBinary, buf[:n]); werr != nil {					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	// 浏览器 → 容器
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			op, payload, err := conn.Read()
			if err != nil {
				return
			}
			if op != wsutil.OpBinary && op != wsutil.OpText {
				continue
			}
			if _, err := session.Write(payload); err != nil {
				return
			}
		}
	}()

	// 任意一头结束就收线：两头都活着但对方已经挂了的连接没有任何用处
	<-done
	_ = session.Close()
	_ = conn.Close()
}

// ---------------------------------------------------------------- 电源

type powerRequest struct {
	Action string `json:"action"` // start / stop / restart
	Force  bool   `json:"force"`  // 仅 stop 有意义：强制停止
}

// handleInstancePower 开机、关机、重启。
//
// 这三个动作都会打断实例里正在跑的东西，所以每一个都要二次确认 ——
// 那件事在前端做，这里只负责校验与执行。刻意不等「起来之后」：
// 开机可能要几十秒，让 HTTP 请求挂在那儿等只会让前端的按钮一直转，
// 而实例状态的恢复本来就由 recon 的下一轮对账自然完成。
func (s *Server) handleInstancePower(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.instanceExists(name) {
		http.Error(w, "实例不存在", http.StatusNotFound)
		return
	}
	if !s.canControlInstance(r, name) {
		http.Error(w, "只能操作自己名下的实例", http.StatusForbidden)
		return
	}
	if s.cfg.Incus == nil {
		http.Error(w, "未连接 Incus", http.StatusServiceUnavailable)
		return
	}

	var req powerRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
		http.Error(w, "请求格式不正确", http.StatusBadRequest)
		return
	}

	var err error
	switch req.Action {
	case "start":
		err = s.cfg.Incus.StartInstance(r.Context(), name)
	case "stop":
		err = s.cfg.Incus.StopInstance(r.Context(), name, req.Force)
	case "restart":
		err = s.cfg.Incus.RestartInstance(r.Context(), name, req.Force)
	default:
		http.Error(w, "动作只支持 start / stop / restart", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, req.Action+" 失败: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.refreshRecon()
	writeJSON(w, http.StatusOK, map[string]string{
		"status":   "已接受",
		"instance": name,
		"action":   req.Action,
		"hint":     powerHint(req.Action),
	})
}

func powerHint(action string) string {
	switch action {
	case "start":
		return "已发出开机指令，实例起来通常要几十秒"
	case "stop":
		return "已发出关机指令，里面正在跑的东西会随之结束"
	default:
		return "已发出重启指令，实例上正在跑的东西会中断"
	}
}

// ---------------------------------------------------------------- 重置口令

type passwordRequest struct {
	Password string `json:"password"`
}

// handleInstancePassword 重置实例的 root 登录口令。
//
// 走容器内 chpasswd，而不是「用 SSH 登进去改」：后者要求先知道旧口令，
// 而「忘了旧口令」正是这个功能要解决的场景。
//
// 口令不落台账、不进日志：这里能看到的只有「改成功了没有」。
func (s *Server) handleInstancePassword(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.instanceExists(name) {
		http.Error(w, "实例不存在", http.StatusNotFound)
		return
	}
	if !s.canControlInstance(r, name) {
		http.Error(w, "只能操作自己名下的实例", http.StatusForbidden)
		return
	}
	if s.cfg.Incus == nil {
		http.Error(w, "未连接 Incus，无法重置口令", http.StatusServiceUnavailable)
		return
	}

	var req passwordRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
		http.Error(w, "请求格式不正确", http.StatusBadRequest)
		return
	}
	if msg := checkPassword(req.Password); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}

	// 用单引号把口令裹起来再交给 sh：口令里出现 $、`、; 这类字符时，
	// 不裹的话它们会被容器里的 shell 当场解释掉，结果是
	// 「设置成功了，但口令不是用户输的那个」。
	// 单引号内的单引号用 '\'' 转义，这是 shell 里唯一可靠的写法。
	quoted := "'" + strings.ReplaceAll(req.Password, "'", `'\''`) + "'"
	cmd := "echo 'root:'" + quoted + " | chpasswd && echo __NEO_PW_OK__"

	out, err := s.cfg.Incus.RunOnce(r.Context(), name, cmd)
	if err != nil {
		http.Error(w, "重置失败: "+err.Error(), http.StatusBadGateway)
		return
	}
	if !strings.Contains(out, "__NEO_PW_OK__") {
		// chpasswd 失败时通常一句话都不说，只回一个非零退出码。
		// 如实报出来，别让前端显示「已重置」而实际没改。
		http.Error(w, "口令未被接受（容器内 chpasswd 未通过）", http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "已重置",
		"hint":   "root 登录口令已更新，请使用新口令",
	})
}

// checkPassword 校验新口令。与申请开通时的随机口令保持同一套要求：
// 太短的口令在一台对着公网的机器上等于没有。
func checkPassword(pw string) string {
	if len([]rune(pw)) < 8 {
		return "口令至少 8 位"
	}
	if len(pw) > 128 {
		return "口令过长"
	}
	if strings.ContainsAny(pw, "\r\n") {
		return "口令不能包含换行"
	}
	return ""
}
