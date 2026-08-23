# Git

## ♻️ 凭据项目级化  260823

迁移后 9 条提交作者仍是旧账号 a6b0x, 重写历史后推送又被 403 拒 —— 从"作者不对"到"认证身份不对"两个独立问题连踩。用户原话依次驱动: "把 edge 历史的提交先改为 0xc1fa" → "vscode 推送...You don't have permissions...Would you like to create a fork?" → "已经通过网页授权完成 但是还是在推送 是网络卡了?" → "将 .git-credentials 的 0xc1fa 移动到项目级别"。

**① 历史重写: 旧账号提交改为 0xc1fa**

```
已删远端库(网页操作) → git filter-branch --env-filter 改 author/committer
    │
    ├─▶ 只对 main 分支, 命中旧邮箱 30930***+a6b0x@ 才替换(不动其他提交)
    ├─▶ 重写后所有 commit hash 变化 —— 预期, 不是异常
    ▼
清理: update-ref -d refs/original/refs/heads/main
    → git reflog expire --expire=now --all
    → git gc --prune=now
    ▼
删除过时 remote-tracking: git branch -r -d origin/main(仍指向已删库的旧历史)
```

- `--env-filter` 里判断 `GIT_AUTHOR_EMAIL` 再 `export` 新的 name/email, 时间戳与提交信息保留
- 备份 ref 存于 `refs/original/`, 确认无误后必须删, 否则旧对象清不掉
- 验证作者分布用 `git log main`; `git log --all` 会把 `origin/main` 的旧历史混进来造成误判

**② 推送 403: VSCode 报 fork 提示的真实含义**

```
[现象] VSCode 推送 → "You don't have permissions to push to 0xc1fa/edge.
                      Would you like to create a fork and push to it instead?"
   │
   ▼ 排查 git credential fill → username=30930***(a6b0x 的 UID)
[根因] 全局 credential.helper=store 命中 ~/.git-credentials 里的 a6b0x github 条目
   │      → 以 a6b0x 认证 → 已无 collaborator 权限 → 403
   │      → VSCode 把 403 翻译成"要不要 fork"(不是真的建议 fork)
   ▼
[解法] 备份 ~/.git-credentials → 删 a6b0x github 条目(保留 solar) → VSCode 网页登录 0xc1fa
```

- **helper 累积坑**: credential helper 按配置顺序全部询问、先命中先用。全局留着旧账号条目, 新账号永远排不上, 项目级文件也白配
- 网页授权后 git 自动把 `https://<uid>:<token>@github.com` 写进全局 `~/.git-credentials`, 用户名是 **UID**(26***) 而非登录名(0xc1fa) —— 反查归属用 `https://api.github.com/user/<uid>`

**③ 推送"卡住"假象: 网络其实是通的**

```
[现象] 网页授权完成后 VSCode 一直"推送中", 怀疑网络卡
   │
   ▼ 诊断: 代理 7890 通(1.8s) / 直连通(0.45s) / push 进程 3 分钟无任何 TCP 连接
[真相] git ls-remote origin → 远端 HEAD 已是本地提交
   │      数据早已上传, 只是收尾进程挂起, VSCode 界面一直转圈
   ▼
处理: kill 挂起进程即可, 不影响已上传数据; 重推会显示 up-to-date
```

- 判定"推没推上去"看**远端 ref**, 不看 VSCode 转圈: `git ls-remote origin` 或 `git fetch origin && git rev-parse origin/main`
- 代理收尾挂起属偶发, 与 note-base.md「TUN vs 端口代理」的网络选型是两码事

**④ 不同项目推不同账号: 项目级凭据方案**

```
                  全局 store                         项目级 store
        ┌──────────────────────────┐      ┌────────────────────────────────┐
        │ ~/.git-credentials       │      │ edge/.git-credentials          │
        │ 只放内网 solar            │      │ 放本项目 github 凭据(0xc1fa)     │
        │ github 条目一律不放        │      │ credential.helper=store       │
        │ credential.helper=store  │      │   --file=<仓库根>/.git-creds    │
        └──────────────────────────┘      └────────────────────────────────┘
            所有项目共用(默认)                    只对本项目生效, 互不干扰
```

实际配置(脚本已复现):

```bash
cd <仓库根>
git config --local credential.helper 'store --file=<仓库根>/.git-credentials'
git remote set-url origin 'https://0xc1fa@github.com/0xc1fa/edge.git'   # 显式带用户名
```

- remote URL 带用户名让 store 精确匹配对应条目, 规避同 host 多条目乱序
- store 文件只放 PAT; **VSCode 网页登录的 token 不写 store**(由 VSCode 保管) —— 纯 VSCode 推送不必配, 命令行免密推送才需要 PAT
- 该文件在仓库内, 已由 `.gitignore` 的 `.git-credentials` 规则保护

**⑤ init/git-config.sh 扩展(供其他项目复用)**

- 默认远程 `a6b0x/edge` → `0xc1fa/edge`
- 新增 `[4/4]` 项目级凭据步骤: 配 `store --file` + 建凭据文件(600) + remote 带用户名(可跳过)
- `detect_gh` 改为**项目级凭据优先、全局兜底**, 支持 UID/用户名两种行格式
- 修两个 bug: 原 `cd $SCRIPT_DIR` 导致脚本只作用于自身目录(曾在测试中污染 edge 配置); URL 贪婪匹配产生 `edge.git.git`
- 敏感文件保护规则加 `.git-credentials`, 任何项目跑脚本都会自动忽略凭据文件

> 卡点: VSCode 登录 token 不落 store 文件, 命令行免密 push 仍需生成 0xc1fa PAT(网页授权只覆盖 VSCode 侧)。

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

**迁移后常见坑: 提交作者/凭据仍是旧账号**

**场景:** push 成功但 GitHub 上 commit 作者仍显示旧账号 a6b0x。

**根因:** GitHub 靠**提交邮箱**关联作者。本地 git 身份(邮箱 `30930***+a6b0x@users.noreply.github.com`, 前缀是 a6b0x 的 user id)没换, 提交就归 a6b0x。仓库归属已迁移, 但作者归属没变——两件事独立。

**排查步骤:**

1. `git remote -v` — 确认 remote 已指向新账号
2. `git log --format='%an <%ae>'` — 看提交邮箱
3. `git config user.name / user.email` — 看本地身份
4. `cat ~/.git-credentials` 脱敏检查 — 看实际认证凭据(用户名 = user id 可反查归属: `https://api.github.com/user/<uid>`)

**修改身份(只影响之后提交, 历史提交不动):**

```bash
# init/git-config.sh 有 detect_gh 自动检测, 也可手动:
git config user.name "H"
git config user.email "26034***+0xc1fa@users.noreply.github.com"  # <uid>+<用户名>@users.noreply.github.com
```

**项目级凭据(只改本项目, 不碰全局):**

```bash
git config --local credential.https://github.com.helper "store --file=<仓库根>/.git-credentials"
printf 'https://0xc1fa:%s@github.com\n' '<PAT>' > <仓库根>/.git-credentials && chmod 600 <仓库根>/.git-credentials
```

- 全局 `~/.git-credentials` 保持不动, 其他项目不受影响
- 该文件在仓库内, 不入 git 跟踪

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
