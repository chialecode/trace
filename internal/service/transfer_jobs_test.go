package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/transfer"
)

func transferTestBackend(t *testing.T) *Backend {
	t.Helper()
	b, err := NewBackend(Options{Locations: testLocations(t), Desktop: NewMockDesktop(), DisableBgWorkers: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func waitTransfer(t *testing.T, b *Backend, id string, want string) TransferJob {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, j := range b.TransferJobs() {
			if j.ID == id && !j.Active() {
				if j.Status != want {
					t.Fatalf("job %s: status %s (want %s), error %q", id, j.Status, want, j.Error)
				}
				return j
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s did not finish", id)
	return TransferJob{}
}

// TestTransferJobsExportEverythingAndImportElsewhere exports more items than
// one page of the library list, then scans and imports the folder into
// another installation through the job API.
func TestTransferJobsExportEverythingAndImportElsewhere(t *testing.T) {
	old := exportPageSize
	exportPageSize = 2
	t.Cleanup(func() { exportPageSize = old })

	src := transferTestBackend(t)
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("BV1tr%05d", i)
		dir := filepath.Join(src.lib.CacheDir(), "bilibili", "1001", "video", id)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		data := []byte("合成视频 " + id)
		if err := os.WriteFile(filepath.Join(dir, "P01.mp4"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		meta := library.ItemMetadata{Version: 1, ID: "video:" + id, Site: "bilibili", Kind: "video", Title: "合成 " + id, Status: "complete",
			Author: library.ItemAuthor{Mid: 1001, Name: "合成作者"},
			Media:  []library.MediaFile{{Role: "video", Path: "P01.mp4", Bytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}}}
		if _, err := src.lib.SaveItem(dir, meta); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := src.StartExport(t.TempDir(), library.FeedFilter{Site: "douyin"}); err == nil {
		t.Fatal("export of an empty range should fail")
	}
	out := filepath.Join(t.TempDir(), "export")
	job, err := src.StartExport(out, library.FeedFilter{})
	if err != nil {
		t.Fatal(err)
	}
	done := waitTransfer(t, src, job.ID, TransferDone)
	if done.Export == nil || done.Export.Items != 5 || done.Export.Files != 5 {
		t.Fatalf("export summary = %+v", done.Export)
	}

	dst := transferTestBackend(t)
	scan, err := dst.StartImportScan(out)
	if err != nil {
		t.Fatal(err)
	}
	ready := waitTransfer(t, dst, scan.ID, TransferReady)
	if ready.Counts[transfer.CategoryNew] != 5 {
		t.Fatalf("scan counts = %v", ready.Counts)
	}
	if _, ok := dst.ImportPlan(scan.ID); !ok {
		t.Fatal("ready job has no plan")
	}
	if _, err := dst.StartImport(scan.ID, transfer.Choices{}); err != nil {
		t.Fatal(err)
	}
	imported := waitTransfer(t, dst, scan.ID, TransferDone)
	if imported.Summary == nil || imported.Summary.Imported != 5 {
		t.Fatalf("import summary = %+v", imported.Summary)
	}
	if _, err := dst.StartImport(scan.ID, transfer.Choices{}); err == nil {
		t.Fatal("a finished plan should not import twice")
	}
	items, err := dst.lib.ListItems(library.FeedFilter{Limit: 50})
	if err != nil || len(items) != 5 {
		t.Fatalf("imported items = %d, %v", len(items), err)
	}

	// A ready plan that is dropped cannot be imported.
	again, err := dst.StartImportScan(out)
	if err != nil {
		t.Fatal(err)
	}
	waitTransfer(t, dst, again.ID, TransferReady)
	if err := dst.CancelTransfer(again.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := dst.ImportPlan(again.ID); ok {
		t.Fatal("dropped plan still available")
	}
	if _, err := dst.StartImport(again.ID, transfer.Choices{}); err == nil {
		t.Fatal("dropped plan imported")
	}
}
