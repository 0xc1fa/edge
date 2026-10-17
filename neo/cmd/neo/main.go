// Command neo 是轻量云主机控制台的服务主体。
//
//	neo serve     启动 HTTP 服务与后台对账
//	neo probe     自检健康接口，非 200 即退出码 1（供 healthcheck 使用）
//	neo version   打印版本
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	// 嵌入时区数据库。
	//
	// GPU 时段（09:00–16:00）与「今天」都按 time.Local 计算。
	// 运行时镜像是 alpine（musl），没有 /usr/share/zoneinfo，
	// 于是 TZ=Asia/Shanghai 会**静默失效**回落到 UTC ——
	// 表现是：管理员以为约的是今天，台账写着昨天；
	// 16:00 自动摘卡发生在北京时间半夜零点。
	// 带上这一行，时区不再依赖镜像里有没有 tzdata。
	_ "time/tzdata"

	"neo/internal/api"
	"neo/internal/auth"
	"neo/internal/catalog"
	"neo/internal/identity"
	"neo/internal/incus"
	"neo/internal/limit"
	"neo/internal/recon"
	"neo/internal/store"
)

// Version 随构建注入，未注入时用 dev 后缀避免误判为某个发布版。
var Version = "0.1.0-dev"

type config struct {
	Socket  string
	Addr    string
	DataDir string
	WebDir  string
	// Interval 是对账周期
	Interval time.Duration
	// SessionTTL 是会话的默认总寿命，MaxIdle 是默认空闲上限。
	// 成员可以各自覆盖，这里的默认值给「没单独设置」的人兜底。
	SessionTTL time.Duration
	MaxIdle    time.Duration
	InviteTTL  time.Duration
	// SecureCookie 与 TrustProxy 都是「部署形态开关」，
	// 默认关是因为默认部署就是本机直曝，打开反而会让会话 cookie 失效。
	SecureCookie bool
	TrustProxy   bool
	GPUTotal     int
	// GPUPCI 是可分配的显卡 PCI 地址。容器里没有 nvidia-smi，
	// 探测不到卡在哪条总线上，所以必须由部署者显式指定。
	GPUPCI []string
	// GPUSweepInterval 是到期巡检的周期
	GPUSweepInterval time.Duration
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		if err := runServe(); err != nil {
			fmt.Fprintf(os.Stderr, "neo serve 失败: %v\n", err)
			os.Exit(1)
		}
	case "probe":
		runProbe()
	case "version":
		fmt.Println(Version)
	case "invite":
		if err := runInvite(); err != nil {
			fmt.Fprintf(os.Stderr, "neo invite 失败: %v\n", err)
			os.Exit(1)
		}
	case "password":
		if err := runPassword(); err != nil {
			fmt.Fprintf(os.Stderr, "neo password 失败: %v\n", err)
			os.Exit(1)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "用法: neo <serve|probe|version|invite|password>")
	fmt.Fprintln(os.Stderr, "  invite   签发一条一次性登录凭证（把人重新放进来时的后路）")
	fmt.Fprintln(os.Stderr, "  password 查看管理员口令，或 --reset 重新生成一个")
}

func loadConfig() config {
	d := func(k, def string) string {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
		return def
	}
	dur := func(k string, def time.Duration) time.Duration {
		v := strings.TrimSpace(os.Getenv(k))
		if v == "" {
			return def
		}
		p, err := time.ParseDuration(v)
		if err != nil {
			return def
		}
		return p
	}
	i := func(k string, def int) int {
		v := strings.TrimSpace(os.Getenv(k))
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return def
		}
		return n
	}
	on := func(k string) bool { return strings.TrimSpace(os.Getenv(k)) == "1" }
	return config{
		Socket:   d(identity.EnvPrefix+"_SOCKET", "/var/lib/incus/unix.socket"),
		Addr:     d(identity.EnvPrefix+"_ADDR", ":5902"),
		DataDir:  d(identity.EnvPrefix+"_DATA_DIR", "./data"),
		WebDir:   d(identity.EnvPrefix+"_WEB_DIR", "./web/dist"),
		Interval: dur(identity.EnvPrefix+"_INTERVAL", 10*time.Second),
		// 会话默认长期有效（0）：到期把内部这几个人踢出去，麻烦多于收益，
		// 而台账是可读可删的 —— 想收紧就单独设时限，或者把那条会话删掉。
		// 填具体时长（如 12h）即回到「到期自动失效」。
		SessionTTL: dur(identity.EnvPrefix+"_SESSION_TTL", 0),
		MaxIdle:    dur(identity.EnvPrefix+"_MAX_IDLE", 0),
		// 凭证默认 15 分钟：够管理员复制链接走到 IM 那头被人点开，
		// 比这更长只是把一次性凭证退回成了一把长期口令。
		InviteTTL:    dur(identity.EnvPrefix+"_INVITE_TTL", 15*time.Minute),
		SecureCookie: on(identity.EnvPrefix + "_SECURE_COOKIE"),
		TrustProxy:   on(identity.EnvPrefix + "_TRUST_PROXY"),
		// 宿主实际装了 2 张 RTX 4090；/1.0/resources 在无 nvidia 运行时上报不到，
		// 所以 GPU 总量做成配置项而不是自动探测，避免以 0 判定成「永远没库存」。
		GPUTotal: i(identity.EnvPrefix+"_GPU_TOTAL", 2),
		// 显卡地址不猜测：猜错就会把同一张卡挂给两个实例，
		// 或者把宿主全部卡交给一个人。没配置时挂载接口会明确拒绝。
		GPUPCI: splitList(os.Getenv(identity.EnvPrefix + "_GPU_PCI")),
		// 到期摘卡的巡检周期：30 秒足够及时（最多晚半分钟），
		// 又不至于频繁打扰 Incus
		GPUSweepInterval: dur(identity.EnvPrefix+"_GPU_SWEEP_INTERVAL", 30*time.Second),
	}
}

// splitList 把 "a,b" 这样的环境变量拆成列表，顺带去掉空白与空项。
func splitList(v string) []string {
	out := make([]string, 0, 2)
	for _, part := range strings.Split(v, ",") {
		p := strings.TrimSpace(part)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func runServe() error {
	cfg := loadConfig()
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// 成员与会话台账。它取代了原先的 secrets.json：
	// 无口令之后不再需要保存口令哈希与会话签名密钥。
	acc, err := store.NewAccess(cfg.DataDir)
	if err != nil {
		return err
	}
	bootstrapAdmin(acc, cfg, log)

	// 兑换限流：凭证是随机 32 字符，理论上不可爆破，
	// 但仍然不该让同一个来源无节制地试。窗口 10 次/分钟，与原先登录一致。
	aut := auth.New(acc, limit.New(10, time.Minute), cfg.SessionTTL, cfg.MaxIdle)
	// 管理员口令：明文落在数据目录里，启动时读一次常驻内存。
	// 没有它，控制台就只剩凭证一条路，把自己锁在门外时就得靠 neo invite 救场。
	loadAdminPassword(aut, cfg, log)

	apps, err := store.NewAppStore(cfg.DataDir)
	if err != nil {
		return err
	}

	// 实例归属台账：哪台机器归谁。与申请台账分开 ——
	// 申请台账只覆盖走控制台开通的实例，而宿主上早有手工建的机器，
	// 没有这张表，那些实例对租户侧的终端/口令/电源永远是灰的。
	owners, err := store.NewOwnerStore(cfg.DataDir)
	if err != nil {
		return err
	}

	// GPU 预约台账。它与申请台账分开：一个是「给我台新机器」，
	// 一个是「把那张卡挂到我已有的机器上用 8 小时」，动作性质不同。
	gpus, err := store.NewGPUStore(cfg.DataDir)
	if err != nil {
		return err
	}

	client := incus.NewClient(cfg.Socket)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	loop := recon.New(client, cfg.Interval, log)
	go loop.Run(ctx)

	// 会话台账的后台维护：周期性落盘 + 清理过期记录。
	// 内存才是权威，落盘只为重启后不必所有人重新登录。
	go acc.Loop(ctx, 30*time.Second, 24*time.Hour)

	// 容忍窗口 = 3 个采集周期 + 10 秒，避免瞬时抖动就判死
	maxAge := 3*cfg.Interval + 10*time.Second
	handler := api.New(api.Config{
		Version: Version,
		Auth:    aut,
		Users:   acc,
		Recon:   loop,
		// 4090 的库存由实测配置注入；resources 接口上报不到显卡，
		// 但我们也只覆盖「本机实有的那一型」，不会凭配置造出一个没装的型号
		Catalog: catalog.Default().WithGPUStock(cfg.GPUTotal),
		Apps:    apps,
		Owners:  owners,
		GPUS:    gpus,
		// 写通道：挂载、摘卡、重启都走它。
		// 这是本项目第一次允许动宿主，范围限定在这三个动作上。
		Incus:  client,
		GPUPCI: cfg.GPUPCI,
		// 成功单据限得更严：这一侧没人值守，被灌垃圾申请会直接污染管理员待办
		ApplyLimit: limit.New(5, time.Hour),
		// 失败尝试放得宽些：表单填错几次不该让人被锁一小时，
		// 但也不为零，否则可以拿它来枚举哪些主机名已存在
		ApplyFailLimit: limit.New(30, time.Hour),
		GPUTotal:       cfg.GPUTotal,
		WebDir:         cfg.WebDir,
		DataDir:        cfg.DataDir,
		MaxAge:         maxAge,
		SecureCookie:   cfg.SecureCookie,
		TrustProxy:     cfg.TrustProxy,
	})

	// 到期自动摘卡。没有它，「8 小时」这个约定就得靠人记得在 16:00 去点一下 ——
	// 那等于没有约定。巡检只认台账里 status=active 的预约，
	// 因此管理员手动摘掉的、被驳回的都不会被它再碰一次。
	go handler.RunGPUSweeper(ctx, cfg.GPUSweepInterval, func(msg string, kv ...any) {
		log.Info(msg, kv...)
	})
	if len(cfg.GPUPCI) == 0 {
		log.Warn("未配置 NEO_GPU_PCI，GPU 挂载将不可用",
			"提示", "填上显卡地址（如 0000:c4:00.0）后重启服务")
	}

	// 时区自检。时段落错 8 小时是很难从界面上看出来的故障：
	// 页面照常显示 09:00–16:00，只是背后算的是另一个时区。
	// 所以启动时把它打出来，人工一眼就能核对。
	now := time.Now()
	zone, offset := now.Zone()
	log.Info("时区", "zone", zone, "UTC偏移小时", float64(offset)/3600, "现在", now.Format("2006-01-02 15:04"))
	if want := strings.TrimSpace(os.Getenv("TZ")); want != "" && !strings.EqualFold(zone, want) && !strings.HasSuffix(zone, "CST") {
		log.Warn("TZ 与实际时区不一致，GPU 时段会按错误的时间执行",
			"TZ", want, "实际", zone)
	}
	{
		s, e, _ := store.GPUWindow(now.Format("2006-01-02"))
		log.Info("GPU 时段", "今天", now.Format("2006-01-02"), "从", s.Format("15:04"), "到", e.Format("15:04"))
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Info("neo 开始监听", "版本", Version, "地址", cfg.Addr, "socket", cfg.Socket)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		log.Info("收到退出信号，正在关闭")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// loadAdminPassword 把管理员口令装进鉴权服务。
//
// 取值优先序：环境变量 NEO_ADMIN_PASSWORD（部署者自己指定）
// → data/admin-password.txt（已经存在的）→ 现生成一个写进去。
//
// 首次生成必须打日志：口令只在那一刻出现在屏幕上，
// 之后它躺在文件里，管理员随时 cat 得到，但当时没看见的人会以为没有口令这回事。
func loadAdminPassword(aut *auth.Service, cfg config, log *slog.Logger) {
	p := strings.TrimSpace(os.Getenv(identity.EnvPrefix + "_ADMIN_PASSWORD"))
	if p != "" {
		aut.SetAdminPassword(p)
		log.Info("管理员口令来自环境变量", "提示", "数据目录里的明文文件不会因此改写")
		return
	}
	got, generated, err := store.EnsureAdminPassword(cfg.DataDir)
	if err != nil {
		log.Error("准备管理员口令失败", "错误", err)
		return
	}
	aut.SetAdminPassword(got)
	if generated {
		log.Warn("已生成管理员口令",
			"口令文件", store.AdminPasswordPath(cfg.DataDir),
			"提示", "cat 出来登录控制台；口令是明文存放的，别外发这个文件")
	}
}

// bootstrapAdmin 在台账还空着（全新部署）时建立第一名管理员，并签发一条登录凭证。
//
// 这是整套登录体系的起点问题：没有口令之后，「第一个人怎么进来」必须有个答案。
// 答案沿用当初 bootstrap-password.txt 的做法 —— 写一份明文到数据目录，
// 由管理员 cat 出来用。区别是这一次它兑换一次即废。
func bootstrapAdmin(acc *store.Access, cfg config, log *slog.Logger) {
	if acc.CountUsers() > 0 {
		return
	}
	u, err := acc.CreateUser(store.UserInput{
		Name: "管理员",
		Role: store.RoleAdmin,
		Note: "首发管理员",
	})
	if err != nil {
		log.Error("创建首发管理员失败", "错误", err)
		return
	}
	inv, err := acc.CreateInvite(u.ID, "", cfg.InviteTTL)
	if err != nil {
		log.Error("签发首发登录凭证失败", "错误", err)
		return
	}
	p := filepath.Join(cfg.DataDir, store.BootstrapFile)
	if err := store.WriteBootstrapInvite(cfg.DataDir, inviteText(inv.Token)); err != nil {
		log.Error("写入首发凭证文件失败", "错误", err)
		return
	}
	log.Warn("已生成首发管理员登录凭证",
		"凭证文件", p,
		"提示", "cat 出来把链接发给使用者即可；兑换一次后自动作废，文件可自行删除")
}

// inviteText 是凭证文件的正文。宿主地址服务并不知情，所以给的是模板加裸 token，
// 让管理员可以直接粘贴进任意聊天窗口。
func inviteText(token string) string {
	return "把下面这行链接发给使用者，点开即完成登录：\n\n" +
		"  http://<宿主IP>:5902/#/login?token=" + token + "\n\n" +
		"凭证（填到登录页也能用）：\n\n  " + token + "\n"
}

// runInvite 在终端里签发一条登录凭证。
//
// 它是后路而不是日常路径：日常在后台页面点「签发」就行。
// 存在的理由很实际 —— 万一把所有人都挡在了门外（比如最后一个管理员被删掉），
// 后台页面本身就进不去了，那时需要一个不依赖登录状态的入口。
//
// 注意：它会直接写数据目录里的台账文件。服务正在运行时，内存中的台账更新不到这里，
// 后续在后台页面上的操作也可能把它覆盖。所以它服务于「服务没起来 / 起不来」的场景。
func runInvite() error {
	fs := flag.NewFlagSet("invite", flag.ExitOnError)
	name := fs.String("name", "", "成员名称；同名的直接复用，不存在则新建")
	role := fs.String("role", store.RoleAdmin, "角色：admin 或 member")
	ttl := fs.Duration("ttl", 15*time.Minute, "凭证有效期，例如 15m、1h")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	if strings.TrimSpace(*name) == "" {
		return errors.New("需要 --name 指定成员名称")
	}

	cfg := loadConfig()
	acc, err := store.NewAccess(cfg.DataDir)
	if err != nil {
		return err
	}

	var target store.User
	for _, u := range acc.ListUsers() {
		if strings.EqualFold(u.Name, strings.TrimSpace(*name)) {
			target = u
			break
		}
	}
	if target.ID == "" {
		target, err = acc.CreateUser(store.UserInput{Name: strings.TrimSpace(*name), Role: *role})
		if err != nil {
			return err
		}
	}
	if target.Status != store.UserStatusActive {
		if _, err := acc.SetUserStatus(target.ID, store.UserStatusActive); err != nil {
			return err
		}
	}
	inv, err := acc.CreateInvite(target.ID, "", *ttl)
	if err != nil {
		return err
	}
	fmt.Println(inviteText(inv.Token))
	return nil
}

// runPassword 查看或重设管理员口令。
//
// 口令明文存在数据目录里，所以「忘了口令」根本不是一个问题：
// cat 出来就是。这条子命令只是让这件事不必记文件路径。
func runPassword() error {
	fs := flag.NewFlagSet("password", flag.ExitOnError)
	reset := fs.Bool("reset", false, "丢弃现有口令，重新生成一个随机的")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}

	cfg := loadConfig()
	path := store.AdminPasswordPath(cfg.DataDir)
	if *reset {
		p, err := store.ResetAdminPassword(cfg.DataDir)
		if err != nil {
			return err
		}
		fmt.Println(p)
		fmt.Fprintf(os.Stderr, "已重设管理员口令，文件：%s\n", path)
		return nil
	}
	p := store.LoadAdminPassword(cfg.DataDir)
	if p == "" {
		var err error
		p, _, err = store.EnsureAdminPassword(cfg.DataDir)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "（此前没有口令文件，已现生成一个）\n")
	}
	fmt.Println(p)
	return nil
}

// runProbe 供 Docker healthcheck 直接使用二进制自身，无需镜像里再装 curl/wget。
func runProbe() {
	cfg := loadConfig()
	addr := cfg.Addr
	if !strings.Contains(addr, ":") {
		addr = ":" + addr
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		port = "5902"
	}
	url := "http://127.0.0.1:" + port + "/api/health"

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "探测失败: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "不健康: HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}
	fmt.Println("healthy")
}
