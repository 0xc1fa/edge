# Git

## 📦 仓库迁移  260823

```
a6b0x/edge ──Transfer ownership──▶ 0xc1fa/edge
    │                                 │
    │  Settings → Danger Zone         │  历史/commit/分支全带走
    │  → Transfer（仅 owner 可见）      ▼
    └──── 对方通知里 Accept ──────▶ 迁移完成
```

**操作要点:**

1. **Settings 菜单只对 owner/admin 显示**: 浏览器登录的账号不是仓库 owner 就看不到 Settings, 先确认右上角登录身份
2. **转移时 GitHub 连内容一起搬**: 全部提交迁移到新账号, 本地不用重推
3. **更新本地 remote**: `git remote set-url origin https://github.com/<新账号>/edge.git`
4. **push 显示 up-to-date 不奇怪**: 迁移时内容已随仓库搬走, 本地/远程 HEAD 一致 → 无数据可传 → 不触发认证。下次有新提交 push 才需要新账号的 token（HTTPS 需新账号 PAT，`credential.helper store` 可免密）

> 迁移期间 git 网络问题（代理配置 / HTTP2 framing layer）见 `note-base.md`「TUN 透明代理 vs 端口代理」「git push 报 HTTP2 framing layer 错误排障」两节。

---

## 🧹 Github 重建仓库  260820

```
push 被拒 → 合并两段独立历史 → 推送成功
    │
    │  ← 但看着 log : 三段历史, 两段是自动建立的
    ▼
删 GitHub 仓库 + 本地 reset --hard 到自己那条提交
    │
    │  ← 删除仓库前先想清楚: 远端没有不可替代的东西(只有自动 README)
    ▼
本地只剩一条干净提交 → 重建同名仓库(不勾任何初始化选项) → 重新推送
```

**重建干净历史的决策框架:**

1. **先确认远端可丢弃**: 删仓库前检查远端内容, 只有 GitHub 自动生成的 README 这类无价值文件才值得删库重建; 有真实提交时应改用 rebase/交互式改写
2. **本地 `reset --hard` 只留自己的提交**: 比 rebase 更彻底——连"曾经有过合并"的痕迹都没有
3. **重建仓库时初始化选项全部不勾**: 勾 README/.gitignore/license 任何一个, 都会再制造一段独立历史, 循环踩坑
4. **删掉过时的 `origin/main` 引用**: `git update-ref -d refs/remotes/origin/main`, 否则 VS Code 里分支状态显示异常(behind/gone)

**真实顾虑:** 为什么宁可删库也不 rebase? 因为 merge 记录、Initial commit 是"自己没写过的东西", 干净的定义是"远端只有我想让它存在的提交", 不是"能推上去就行"。
