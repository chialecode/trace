package remoteserver

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/upload"
)

// RunOptions configure trace-server run.
type RunOptions struct {
	DataDir string
	// Listener replaces listening on server.json's address (tests).
	Listener net.Listener
	// Backend adjusts the backend options (tests point the site clients at
	// fake sites here).
	Backend func(*service.Options)
	// LogMirror receives the log lines too (standard error under systemd).
	LogMirror io.Writer
	// UploadEnv is rclone's environment; the process's when nil.
	UploadEnv []string
	// Ready is called with the listening address once requests are served.
	Ready func(addr string)
	// ExitTimeout bounds the finishing of tasks and recordings (30 s).
	ExitTimeout time.Duration
}

// Run starts the backend in remote mode, the uploader and the HTTPS API,
// and serves until ctx ends; then it finishes like 退出 in the tray (tasks
// paused, recordings finalized) within ExitTimeout and returns nil.
func Run(ctx context.Context, opt RunOptions) error {
	cfg, err := LoadConfig(opt.DataDir)
	if err != nil {
		return err
	}
	lock, err := appdirs.LockDataDir(opt.DataDir)
	if err != nil {
		return fmt.Errorf("数据目录正被另一个 trace-server 使用：%w", err)
	}
	defer lock.Release()
	tlsCfg, err := TLSConfig(opt.DataDir)
	if err != nil {
		return fmt.Errorf("读取证书失败：%w", err)
	}
	if opt.ExitTimeout <= 0 {
		opt.ExitTimeout = 30 * time.Second
	}

	bo := service.Options{
		Locations: service.RemoteLocations(opt.DataDir, cfg.ExportDir),
		Desktop:   service.NewMockDesktop(),
		Profile:   service.ProfileRemote,
		NodeName:  cfg.Name,
		LogMirror: opt.LogMirror,
	}
	if opt.Backend != nil {
		opt.Backend(&bo)
	}
	backend, err := service.NewBackend(bo)
	if err != nil {
		return err
	}
	disk := DiskSpace(cfg.ExportDir)
	backend.SetCacheGate(CacheGate(disk))

	up, err := upload.New(upload.Options{
		Config: cfg.Upload, Library: backend.LibraryStore(), DB: backend.LibraryDB(), DataDir: opt.DataDir,
		Busy: backend.ItemBusy, Env: opt.UploadEnv, Logf: backend.Logf,
	})
	if err != nil {
		_ = backend.Close()
		return err
	}
	backend.LibraryStore().OnSaved(up.Notify)
	upCtx, stopUpload := context.WithCancel(context.Background())
	go up.Run(upCtx)

	ln := opt.Listener
	if ln == nil {
		ln, err = net.Listen("tcp", cfg.Listen)
		if err != nil {
			stopUpload()
			up.Wait()
			_ = backend.Close()
			return fmt.Errorf("无法监听 %s：%w", cfg.Listen, err)
		}
	}
	srv := New(Options{DataDir: opt.DataDir, TokenHash: cfg.TokenHash, Service: backend, Uploader: up, Disk: disk, Logf: backend.Logf})
	backend.Logf("trace-server listening: %s upload=%s", ln.Addr().String(), cfg.Upload.Mode)
	serveCtx, stopServe := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- srv.Serve(serveCtx, ln, tlsCfg) }()
	if opt.Ready != nil {
		opt.Ready(ln.Addr().String())
	}

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-served:
	}
	backend.Logf("trace-server stopping")
	stopServe()
	if serveErr == nil {
		serveErr = <-served
	}
	if !backend.ExitGracefully(opt.ExitTimeout) {
		backend.Logf("trace-server: tasks did not finish within %s", opt.ExitTimeout)
	}
	stopUpload()
	up.Wait()
	closeErr := backend.Close()
	if serveErr != nil {
		return serveErr
	}
	return closeErr
}
