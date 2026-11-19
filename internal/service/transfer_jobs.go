package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/transfer"
)

// TransferService is 媒体库 › 导入… / 导出… (docs/design/remote.md §6.3,
// §6.4): background jobs listed on the Tasks page under 导入.
type TransferService interface {
	// StartImportScan scans a folder (read-only) in the background; the
	// job becomes "ready" with a plan to confirm.
	StartImportScan(path string) (TransferJob, error)
	// ImportPlan returns the plan of a scanned job.
	ImportPlan(jobID string) (transfer.Plan, bool)
	// StartImport imports a scanned plan with the user's answers.
	StartImport(jobID string, choices transfer.Choices) (TransferJob, error)
	// StartExport copies the items of a media library filter (everything
	// when empty; Limit and Offset are ignored) into a folder.
	StartExport(target string, filter library.FeedFilter) (TransferJob, error)
	TransferJobs() []TransferJob
	// CancelTransfer stops a scan, import or export; a ready plan is
	// dropped.
	CancelTransfer(jobID string) error
}

var _ TransferService = (*Backend)(nil)

// Transfer job states.
const (
	TransferScanning = "scanning"
	TransferReady    = "ready"
	TransferRunning  = "running"
	TransferDone     = "done"
	TransferFailed   = "failed"
	TransferCanceled = "canceled"
)

// TransferJob is one import or export.
type TransferJob struct {
	ID         string                    `json:"id"`
	Kind       string                    `json:"kind"` // "import" or "export"
	Status     string                    `json:"status"`
	Source     string                    `json:"source,omitempty"`
	Target     string                    `json:"target,omitempty"`
	Progress   transfer.Progress         `json:"progress"`
	SpeedBps   int64                     `json:"speedBps"`
	Counts     map[transfer.Category]int `json:"counts,omitempty"`
	Summary    *transfer.Summary         `json:"summary,omitempty"`
	Export     *transfer.ExportSummary   `json:"export,omitempty"`
	Error      string                    `json:"error,omitempty"`
	StartedAt  string                    `json:"startedAt"`
	FinishedAt string                    `json:"finishedAt,omitempty"`
}

// Active reports whether the job is still working.
func (j TransferJob) Active() bool {
	return j.Status == TransferScanning || j.Status == TransferRunning
}

type transferJobs struct {
	mu   sync.Mutex
	seq  int
	jobs map[string]*transferJobState
}

type transferJobState struct {
	job      TransferJob
	plan     *transfer.Plan
	cancel   context.CancelFunc
	lastAt   time.Time
	lastByte int64
	lastPub  time.Time
}

func (b *Backend) transfers() *transferJobs {
	b.transferOnce.Do(func() { b.transferState = &transferJobs{jobs: map[string]*transferJobState{}} })
	return b.transferState
}

func cloneTransferJob(j TransferJob) TransferJob {
	if len(j.Counts) > 0 {
		counts := make(map[transfer.Category]int, len(j.Counts))
		for k, v := range j.Counts {
			counts[k] = v
		}
		j.Counts = counts
	}
	if j.Summary != nil {
		s := *j.Summary
		if len(s.Errors) > 0 {
			s.Errors = append([]string(nil), s.Errors...)
		}
		j.Summary = &s
	}
	if j.Export != nil {
		e := *j.Export
		if len(e.Errors) > 0 {
			e.Errors = append([]string(nil), e.Errors...)
		}
		j.Export = &e
	}
	return j
}

func (b *Backend) newTransferJob(kind, source, target, status string) (*transferJobState, TransferJob, context.Context, context.CancelFunc) {
	t := b.transfers()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq++
	ctx, cancel := context.WithCancel(b.bgCtx)
	st := &transferJobState{job: TransferJob{ID: fmt.Sprintf("%s-%d-%d", kind, time.Now().Unix(), t.seq), Kind: kind, Status: status, Source: source, Target: target,
		StartedAt: time.Now().UTC().Format(time.RFC3339)}, cancel: cancel, lastAt: time.Now()}
	t.jobs[st.job.ID] = st
	return st, cloneTransferJob(st.job), ctx, cancel
}

func (b *Backend) hasActiveTransferJobs() bool {
	t := b.transfers()
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, st := range t.jobs {
		if st.job.Active() {
			return true
		}
	}
	return false
}

func (b *Backend) updateTransfer(st *transferJobState, fn func(j *TransferJob)) {
	t := b.transfers()
	t.mu.Lock()
	fn(&st.job)
	id := st.job.ID
	now := time.Now()
	publish := !st.job.Active() || now.Sub(st.lastPub) > 250*time.Millisecond
	if publish {
		st.lastPub = now
	}
	t.mu.Unlock()
	if publish {
		b.events.Publish(EventTransferUpdated, id)
	}
}

func (b *Backend) transferProgress(st *transferJobState) func(transfer.Progress) {
	return func(p transfer.Progress) {
		b.updateTransfer(st, func(j *TransferJob) {
			j.Progress = p
			now := time.Now()
			if dt := now.Sub(st.lastAt); dt >= time.Second {
				j.SpeedBps = int64(float64(p.Bytes-st.lastByte) / dt.Seconds())
				st.lastAt, st.lastByte = now, p.Bytes
			}
		})
	}
}

func (b *Backend) finishTransfer(st *transferJobState, err error, fn func(j *TransferJob)) {
	b.updateTransfer(st, func(j *TransferJob) {
		if fn != nil {
			fn(j)
		}
		j.SpeedBps = 0
		j.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		switch {
		case errors.Is(err, context.Canceled):
			j.Status = TransferCanceled
		case err != nil:
			j.Status, j.Error = TransferFailed, b.sanitizeLogLine(err.Error())
		case j.Status == TransferScanning:
			j.Status = TransferReady
		default:
			j.Status = TransferDone
		}
		if j.Status != TransferReady {
			st.plan = nil
		}
	})
}

// StartImportScan scans path in the background.
func (b *Backend) StartImportScan(path string) (TransferJob, error) {
	if err := b.requireDesktop(); err != nil {
		return TransferJob{}, err
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return TransferJob{}, errors.New("请填写要导入的文件夹")
	}
	st, job, ctx, cancel := b.newTransferJob("import", path, b.lib.CacheDir(), TransferScanning)
	started := b.goBg(func(context.Context) {
		defer cancel()
		plan, err := transfer.Scan(ctx, path, b.lib, b.transferProgress(st))
		b.finishTransfer(st, err, func(j *TransferJob) {
			if err == nil {
				st.plan = &plan
				j.Counts = plan.Counts
			}
		})
		if err == nil {
			b.logInfo(fmt.Sprintf("import scanned: items=%d", len(plan.Entries)))
		}
	})
	if !started {
		cancel()
		b.finishTransfer(st, context.Canceled, nil)
		return TransferJob{}, errors.New("后端正在关闭")
	}
	return job, nil
}

// ImportPlan returns a scanned job's plan.
func (b *Backend) ImportPlan(jobID string) (transfer.Plan, bool) {
	t := b.transfers()
	t.mu.Lock()
	defer t.mu.Unlock()
	st, ok := t.jobs[jobID]
	if !ok || st.plan == nil {
		return transfer.Plan{}, false
	}
	return *st.plan, true
}

// StartImport imports a ready plan.
func (b *Backend) StartImport(jobID string, choices transfer.Choices) (TransferJob, error) {
	if err := b.requireDesktop(); err != nil {
		return TransferJob{}, err
	}
	t := b.transfers()
	t.mu.Lock()
	st, ok := t.jobs[jobID]
	if !ok || st.plan == nil || st.job.Status != TransferReady {
		t.mu.Unlock()
		return TransferJob{}, errors.New("这次扫描已失效，请重新扫描")
	}
	plan := *st.plan
	prevCancel := st.cancel
	ctx, cancel := context.WithCancel(b.bgCtx)
	st.cancel = cancel
	st.job.Status = TransferRunning
	st.job.FinishedAt = ""
	st.job.Progress = transfer.Progress{}
	st.job.SpeedBps = 0
	st.job.Error = ""
	st.lastAt, st.lastByte = time.Now(), 0
	job := cloneTransferJob(st.job)
	t.mu.Unlock()
	if prevCancel != nil {
		prevCancel()
	}
	b.events.Publish(EventTransferUpdated, jobID)
	started := b.goBg(func(context.Context) {
		defer cancel()
		sum, err := transfer.Apply(ctx, plan, choices, b.lib, b.transferProgress(st))
		b.finishTransfer(st, err, func(j *TransferJob) { s := sum; j.Summary = &s })
		b.logInfo(fmt.Sprintf("import finished: new=%d supplemented=%d conflicts=%d incomplete=%d files=%d errors=%d", sum.Imported, sum.Supplemented, sum.Conflicts, sum.Incomplete, sum.Files, len(sum.Errors)))
		b.events.Publish(EventLibraryChanged, "import")
	})
	if !started {
		cancel()
		b.finishTransfer(st, context.Canceled, nil)
		return TransferJob{}, errors.New("后端正在关闭")
	}
	return job, nil
}

// StartExport exports the items of filter into target.
func (b *Backend) StartExport(target string, filter library.FeedFilter) (TransferJob, error) {
	if err := b.requireDesktop(); err != nil {
		return TransferJob{}, err
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return TransferJob{}, errors.New("请填写导出到的文件夹")
	}
	items, err := exportItems(b.lib, filter)
	if err != nil {
		return TransferJob{}, err
	}
	if len(items) == 0 {
		return TransferJob{}, errors.New("当前范围内没有可导出的内容")
	}
	st, job, ctx, cancel := b.newTransferJob("export", b.lib.CacheDir(), target, TransferRunning)
	started := b.goBg(func(context.Context) {
		defer cancel()
		sum, err := transfer.Export(ctx, items, b.lib, target, b.transferProgress(st))
		b.finishTransfer(st, err, func(j *TransferJob) { s := sum; j.Export = &s })
		b.logInfo(fmt.Sprintf("export finished: items=%d files=%d errors=%d", sum.Items, sum.Files, len(sum.Errors)))
	})
	if !started {
		cancel()
		b.finishTransfer(st, context.Canceled, nil)
		return TransferJob{}, errors.New("后端正在关闭")
	}
	return job, nil
}

// exportPageSize is how many items exportItems reads at a time.
var exportPageSize = 500

// exportItems lists every item of filter, page by page (ListItems returns
// at most one page).
func exportItems(lib interface {
	ListItems(library.FeedFilter) ([]library.ItemMetadata, error)
}, filter library.FeedFilter) ([]library.ItemMetadata, error) {
	page := exportPageSize
	filter.Limit, filter.Sort = page, "oldest"
	var out []library.ItemMetadata
	for offset := 0; ; offset += page {
		filter.Offset = offset
		items, err := lib.ListItems(filter)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if len(items) < page {
			return out, nil
		}
	}
}

// TransferJobs lists the jobs of this run, newest first.
func (b *Backend) TransferJobs() []TransferJob {
	t := b.transfers()
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]TransferJob, 0, len(t.jobs))
	for _, st := range t.jobs {
		out = append(out, cloneTransferJob(st.job))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt != out[j].StartedAt {
			return out[i].StartedAt > out[j].StartedAt
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// CancelTransfer stops a job, or drops a ready plan.
func (b *Backend) CancelTransfer(jobID string) error {
	t := b.transfers()
	t.mu.Lock()
	st, ok := t.jobs[jobID]
	if !ok {
		t.mu.Unlock()
		return nil
	}
	if st.job.Status == TransferReady {
		st.job.Status, st.plan = TransferCanceled, nil
		st.job.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	}
	cancel := st.cancel
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	b.events.Publish(EventTransferUpdated, jobID)
	return nil
}
