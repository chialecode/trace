package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/tools/internal/build"
)

// serverArchs are the Linux architectures trace-server is built for.
var serverArchs = []string{"amd64", "arm64"}

// serverDeployDir holds the files shipped beside the program.
const serverDeployDir = "cmd/trace-server/deploy"

// buildServer cross-compiles trace-server (CGO_ENABLED=0, -trimpath, the
// desktop's version injection) for each architecture and publishes one
// trace-server-<version>-linux-<arch>.tar.gz per architecture plus
// SHA256SUMS into dist/trace-server/, replacing the previous packages
// (docs/design/remote.md §2, DEC-025).
func buildServer(now time.Time) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	info, _, err := devInfo(root, now)
	if err != nil {
		return err
	}
	if err := os.MkdirAll("dist", 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp("dist", ".staging-server-"+now.UTC().Format("20060102-150405")+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	notices := filepath.Join(staging, "THIRD-PARTY-NOTICES.txt")
	if err := build.ThirdPartyNotices(root, notices); err != nil {
		return fmt.Errorf("generate THIRD-PARTY-NOTICES.txt: %w", err)
	}
	var archives []string
	for _, arch := range serverArchs {
		name := fmt.Sprintf("trace-server-%s-linux-%s", info.Version, arch)
		bin := filepath.Join(staging, "trace-server-"+arch)
		fmt.Printf("==> [package] building %s (%s)...\n", name, info.Commit)
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w "+build.LDFlags(info, "", ""), "-o", bin, "./cmd/trace-server")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch, "GOFLAGS=-buildvcs=false")
		appdirs.SetSubprocessQuiet(cmd)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("go build linux/%s: %w\n%s", arch, err, out)
		}
		st, err := os.Stat(bin)
		if err != nil || st.Size() == 0 {
			return fmt.Errorf("missing or empty trace-server for %s: %v", arch, err)
		}
		version := serverVersionFile(info, arch)
		files := []tarFile{
			{name: "trace-server", path: bin, mode: 0o755},
			{name: "trace-server.service", path: filepath.Join(root, filepath.FromSlash(serverDeployDir), "trace-server.service"), mode: 0o644},
			{name: "README.md", path: filepath.Join(root, filepath.FromSlash(serverDeployDir), "README.md"), mode: 0o644},
			{name: "THIRD-PARTY-NOTICES.txt", path: notices, mode: 0o644},
			{name: "VERSION.txt", data: []byte(version), mode: 0o644},
		}
		archive := name + ".tar.gz"
		if err := writeTarGz(filepath.Join(staging, archive), name, files, now); err != nil {
			return err
		}
		fmt.Printf("    trace-server linux/%s: %s\n", arch, mib(st.Size()))
		archives = append(archives, archive)
	}
	sums, err := build.WriteSums(staging, "SHA256SUMS", archives)
	if err != nil {
		return err
	}
	dest := filepath.Join("dist", "trace-server")
	if err := build.PublishBundleWith(staging, dest, "SHA256SUMS", append(archives, "SHA256SUMS"), true); err != nil {
		return err
	}
	fmt.Printf("==> [package] staged and verified into %s:\n%s", dest, sums)
	return nil
}

func mib(n int64) string { return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20)) }

func serverVersionFile(info build.Info, arch string) string {
	return fmt.Sprintf("version=%s\nchannel=%s\ncommit=%s\nbuilt_at=%s\nos=linux\narch=%s\n", info.Version, info.Channel, info.Commit, info.BuildTime, arch)
}

// tarFile is one file of a package: read from path, or data.
type tarFile struct {
	name string
	path string
	data []byte
	mode int64
}

// writeTarGz writes files under the folder dir into a gzip-compressed tar,
// owned by root with fixed times, so unpacking with --strip-components=1
// puts them in the target folder.
func writeTarGz(out, dir string, files []tarFile, now time.Time) (err error) {
	f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(out)
		}
	}()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	mtime := now.UTC().Truncate(time.Second)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: dir + "/", Mode: 0o755, ModTime: mtime, Format: tar.FormatPAX}); err != nil {
		return err
	}
	for _, tf := range files {
		data := tf.data
		if tf.path != "" {
			if data, err = os.ReadFile(tf.path); err != nil {
				return err
			}
		}
		if strings.HasSuffix(tf.name, ".service") || strings.HasSuffix(tf.name, ".md") || strings.HasSuffix(tf.name, ".txt") {
			data = []byte(strings.ReplaceAll(string(data), "\r\n", "\n"))
		}
		hdr := &tar.Header{Typeflag: tar.TypeReg, Name: dir + "/" + tf.name, Mode: tf.mode, Size: int64(len(data)), ModTime: mtime, Format: tar.FormatPAX}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}
