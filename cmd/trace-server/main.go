// Command trace-server is TRACE's headless remote for a Linux VPS
// (docs/design/remote.md §2): it checks the subscriptions the desktop
// syncs, caches their new content by rule, records lives and uploads the
// finished files with rclone. It has no library, parsing or player and
// never links the desktop UI.
//
//	trace-server init --data <dir> --export <dir> [--listen :47380] [--public-address host[:port]] [--name <name>]
//	trace-server run --data <dir>
//	trace-server token --data <dir>
//	trace-server upload-test --data <dir>
//	trace-server rclone-filter
//	trace-server version
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/remoteserver"
	"github.com/chialecode/trace/internal/upload"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

const usage = `trace-server：TRACE 远程版

用法：
  trace-server init --data <数据目录> --export <导出目录> [--listen :47380] [--public-address 地址[:端口]] [--name 名字]
  trace-server run --data <数据目录>
  trace-server token --data <数据目录>
  trace-server upload-test --data <数据目录>
  trace-server rclone-filter
  trace-server version
`

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "version", "-v", "--version":
		if len(rest) > 0 {
			fmt.Fprintln(stderr, "version 不接受额外参数")
			return 2
		}
		b := appdirs.BuildInfo()
		fmt.Fprintf(stdout, "trace-server %s\ncommit %s\nbuilt %s\n", b.Version, orDash(b.Commit), orDash(b.BuildTime))
		return 0
	case "rclone-filter":
		if len(rest) > 0 {
			fmt.Fprintln(stderr, "rclone-filter 不接受额外参数")
			return 2
		}
		fmt.Fprint(stdout, rcloneFilter)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	case "init", "run", "token", "upload-test":
	default:
		fmt.Fprint(stderr, usage)
		return 2
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	data := fs.String("data", "", "数据目录")
	var export, listen, public, name *string
	if cmd == "init" {
		export = fs.String("export", "", "导出目录（init）")
		listen = fs.String("listen", "", "监听地址（init，默认 :47380）")
		public = fs.String("public-address", "", "写进连接码的地址（init，默认本机第一个非回环地址）")
		name = fs.String("name", "", "名字（init，默认主机名）")
	}
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "多余的参数：%s\n", fs.Arg(0))
		return 2
	}
	if *data == "" {
		fmt.Fprintln(stderr, "需要 --data <数据目录>")
		return 2
	}
	switch cmd {
	case "init":
		res, err := remoteserver.Init(remoteserver.InitOptions{DataDir: *data, ExportDir: *export, Listen: *listen, PublicAddress: *public, Name: *name})
		if err != nil {
			fmt.Fprintln(stderr, "init 失败：", err)
			return 1
		}
		printCode(stdout, *data, res, true)
		return 0
	case "token":
		res, err := remoteserver.RotateToken(*data)
		if err != nil {
			fmt.Fprintln(stderr, "换令牌失败：", err)
			return 1
		}
		fmt.Fprintln(stdout, "已换新令牌，旧令牌立即失效。")
		printCode(stdout, *data, res, false)
		return 0
	case "upload-test":
		cfg, err := remoteserver.LoadConfig(*data)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		up, err := upload.New(upload.Options{Config: cfg.Upload, DataDir: *data})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := up.Test(ctx); err != nil {
			fmt.Fprintln(stderr, "上传测试失败：", err)
			return 1
		}
		fmt.Fprintf(stdout, "上传测试通过：%s 可以写入并删除。\n", cfg.Upload.Remote)
		return 0
	case "run":
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
		defer stop()
		err := remoteserver.Run(ctx, remoteserver.RunOptions{DataDir: *data, LogMirror: stderr, Ready: func(addr string) {
			fmt.Fprintf(stderr, "trace-server 已启动：%s\n", addr)
		}})
		if err != nil {
			fmt.Fprintln(stderr, "trace-server：", err)
			return 1
		}
		return 0
	}
	return 2
}

func printCode(w io.Writer, dataDir string, res remoteserver.InitResult, first bool) {
	if first {
		fmt.Fprintf(w, "已初始化：数据目录 %s，导出目录 %s，监听 %s\n\n", dataDir, res.Config.ExportDir, res.Config.Listen)
	}
	fmt.Fprintln(w, "连接码（只显示这一次，请复制到本地 TRACE 的“设置 › 远程”）：")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  "+res.Code)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "地址：%s\n证书指纹（SHA-256）：%s\n", res.Conn.URL, res.Conn.Fingerprint)
	fmt.Fprintln(w, "连接码里的地址按本机的网卡生成；VPS 的公网地址不同时，请用 --public-address 重新 init，或在本地分别填写地址、令牌与指纹。")
	if first {
		fmt.Fprintln(w)
		fmt.Fprint(w, rcloneFilter)
	}
}

const rcloneFilter = `上传时排除未写完的文件（自己定时运行 rclone 时加上）：
  --exclude "*.part" --exclude ".trace-*" --min-age 2m
例如：
  rclone move <导出目录> gdrive:trace --exclude "*.part" --exclude ".trace-*" --min-age 2m --delete-empty-src-dirs
`

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
