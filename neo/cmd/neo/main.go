// Command neo 是轻量云主机控制台的服务主体。
//
//	neo serve     启动 HTTP 服务与后台对账
//	neo probe     自检健康接口，非 200 即退出码 1（供 healthcheck 使用）
//	neo version   打印版本
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

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
	Socket     string
	Addr       string
	DataDir    string
	WebDir     string
	Interval   time.Duration
	SessionTTL time.Duration
	AdminPW    string
	GPUTotal   int
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
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "用法: neo <serve|probe|version>")
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
	return config{
		Socket:     d(identity.EnvPrefix+"_SOCKET", "/var/lib/incus/unix.socket"),
		Addr:       d(identity.EnvPrefix+"_ADDR", ":5902"),
		DataDir:    d(identity.EnvPrefix+"_DATA_DIR", "./data"),
		WebDir:     d(identity.EnvPrefix+"_WEB_DIR", "./web/dist"),
		Interval:   dur(identity.EnvPrefix+"_INTERVAL", 10*time.Second),
		SessionTTL: dur(identity.EnvPrefix+"_SESSION_TTL", 12*time.Hour),
		AdminPW:    d(identity.EnvPrefix+"_ADMIN_PASSWORD", ""),
		// 宿主实际装了 2 张 RTX 4090；/1.0/resources 在无 nvidia 运行时上报不到，
		// 所以 GPU 总量做成配置项而不是自动探测，避免以 0 判定成「永远没库存」。
		GPUTotal: i(identity.EnvPrefix+"_GPU_TOTAL", 2),
	}
}

func runServe() error {
	cfg := loadConfig()
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// 首次启动生成管理员口令：写一份明文到数据目录，由管理员自行留存或删除
	sec, generated, err := store.LoadOrCreate(cfg.DataDir, cfg.AdminPW)
	if err != nil {
		return err
	}
	if generated != "" {
		if err := store.WriteBootstrap(cfg.DataDir, generated); err == nil {
			log.Warn("首发管理员口令已生成",
				"口令文件", cfg.DataDir+"/bootstrap-password.txt",
				"提示", "用它在登录页认证即成为平台管理员；该文件由管理员自行管理")
		}
	}

	aut, err := auth.New(sec, 10, time.Minute)
	if err != nil {
		return err
	}

	apps, err := store.NewAppStore(cfg.DataDir)
	if err != nil {
		return err
	}

	client := incus.NewClient(cfg.Socket)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	loop := recon.New(client, cfg.Interval, log)
	go loop.Run(ctx)

	// 容忍窗口 = 3 个采集周期 + 10 秒，避免瞬时抖动就判死
	maxAge := 3*cfg.Interval + 10*time.Second
	handler := api.New(api.Config{
		Version:    Version,
		Auth:       aut,
		Recon:      loop,
		// 4090 的库存由实测配置注入；resources 接口上报不到显卡，
		// 但我们也只覆盖「本机实有的那一型」，不会凭配置造出一个没装的型号
		Catalog: catalog.Default().WithGPUStock(cfg.GPUTotal),
		Apps:       apps,
		// 成功单据限得更严：这一侧没人值守，被灌垃圾申请会直接污染管理员待办
		ApplyLimit: limit.New(5, time.Hour),
		// 失败尝试放得宽些：表单填错几次不该让人被锁一小时，
		// 但也不为零，否则可以拿它来枚举哪些主机名已存在
		ApplyFailLimit: limit.New(30, time.Hour),
		GPUTotal:       cfg.GPUTotal,
		WebDir:     cfg.WebDir,
		DataDir:    cfg.DataDir,
		SessionTTL: cfg.SessionTTL,
		MaxAge:     maxAge,
	})

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
