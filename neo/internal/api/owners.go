package api

import (
	"encoding/json"
	"net/http"
	"time"

	"neo/internal/store"
)

// 实例归属的分配与解除。
//
// 只给管理员：这件事决定「谁能动哪台机器」，等于在发钥匙。
// 而钥匙发出去之后，前端只按归属显示按钮，服务端再按归属校验一次 ——
// 两处都看同一张台账，没有第二份真相。
//
// 为什么要有个页面来做这件事：宿主上早有手工建的实例，申请台账里没有它们，
// 于是租户侧的网页终端、改口令、开关机对谁都是灰的。分配一次就把它们
// 纳进体系，不必为了用上这些功能去重新申请开通一台机器。

type assignRequest struct {
	Owner string `json:"owner"`
}

// handleInstanceAssign 把一台实例分给某个成员。
func (s *Server) handleInstanceAssign(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.instanceExists(name) {
		http.Error(w, "实例不存在", http.StatusNotFound)
		return
	}
	if s.cfg.Owners == nil {
		http.Error(w, "归属台账未启用", http.StatusServiceUnavailable)
		return
	}

	var req assignRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
		http.Error(w, "请求格式不正确", http.StatusBadRequest)
		return
	}
	owner := ""
	for _, u := range s.cfg.Users.ListUsers() {
		if u.Name == req.Owner {
			owner = u.Name
			break
		}
	}
	if owner == "" {
		// 必须是已存在的成员：分配一个查不到的名字，等于把机器分给一个
		// 谁都登不进来的账号，而页面上只会显示一个看起来正常的名子。
		http.Error(w, "成员不存在，请先在「成员」页建好再分配", http.StatusBadRequest)
		return
	}

	by := ""
	if view, state := s.current(r); state == store.SessionValid {
		by = view.UserName
	}
	rec, err := s.cfg.Owners.Set(name, owner, by, time.Now())
	if err != nil {
		http.Error(w, "分配失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.refreshRecon()
	writeJSON(w, http.StatusOK, map[string]any{
		"instance": name,
		"owner":    rec.Owner,
		"status":   "已分配",
		"hint":     name + " 现在归 " + rec.Owner + "，对方刷新后即可使用终端、口令与电源",
	})
}

// handleInstanceUnassign 解除归属。
//
// 解除之后这台机器回到「未登记属主」：租户侧按钮重新变灰，
// 管理员仍可再分配。这是把钥匙收回来，不是把机器删掉。
func (s *Server) handleInstanceUnassign(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.instanceExists(name) {
		http.Error(w, "实例不存在", http.StatusNotFound)
		return
	}
	if s.cfg.Owners == nil {
		http.Error(w, "归属台账未启用", http.StatusServiceUnavailable)
		return
	}
	if err := s.cfg.Owners.Delete(name); err != nil {
		http.Error(w, "解除失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.refreshRecon()
	writeJSON(w, http.StatusOK, map[string]any{
		"instance": name,
		"status":   "已解除",
		"hint":     name + " 已回到未登记属主，租户侧的操作入口随之关闭",
	})
}

// handleInstanceOwners 列出全部分配，供管理员核对「谁手里有哪些机器」。
func (s *Server) handleInstanceOwners(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Owners == nil {
		writeJSON(w, http.StatusOK, map[string]any{"owners": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"owners": s.cfg.Owners.List()})
}
