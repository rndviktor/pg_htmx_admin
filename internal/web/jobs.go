package web

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

// Background jobs for backup/restore commands. A job runs in its own goroutine
// with its own timeout context, so closing the modal (or the browser) does not
// kill a long dump. State is in memory only: jobs are lost on restart, and only
// the newest maxJobs are kept. Progress is the tool's --verbose log tail plus
// elapsed time and the growing size of the output file/directory; the tools
// report no percentage.

const maxJobs = 50

const (
	jobRunning   = "running"
	jobDone      = "done"
	jobFailed    = "failed"
	jobCancelled = "cancelled"
)

// tailBuffer is an io.Writer that keeps only the most recent output.
type tailBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > 2*maxToolOutput {
		t.b = append([]byte(nil), t.b[len(t.b)-maxToolOutput:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	b := t.b
	if len(b) > maxToolOutput {
		b = b[len(b)-maxToolOutput:]
	}
	return string(b)
}

type job struct {
	ID      string
	Label   string
	Command string
	Out     string // dump output path, "" for restores
	Started time.Time
	out     tailBuffer
	cancel  context.CancelFunc

	mu       sync.Mutex
	status   string
	errMsg   string
	finished time.Time
}

var (
	jobsMu    sync.Mutex
	jobsByID  = map[string]*job{}
	jobsOrder []string // oldest first
	jobSeq    int
)

// startJob launches the command in the background and registers it.
func startJob(label string, cmd backupCmd, environ []string) (*job, error) {
	bin, err := exec.LookPath(cmd.Tool)
	if err != nil {
		return nil, fmt.Errorf("%s was not found on PATH; install the PostgreSQL client tools", cmd.Tool)
	}
	ctx, cancel := context.WithTimeout(context.Background(), backupTimeout)
	j := &job{
		Label: label, Command: toolPreview(cmd.Tool, cmd.Args), Out: cmd.Out,
		Started: time.Now(), cancel: cancel, status: jobRunning,
	}
	c := exec.CommandContext(ctx, bin, cmd.Args...)
	c.Env = environ
	c.Stdout, c.Stderr = &j.out, &j.out
	if err := c.Start(); err != nil {
		cancel()
		return nil, err
	}

	jobsMu.Lock()
	jobSeq++
	j.ID = strconv.Itoa(jobSeq)
	jobsByID[j.ID] = j
	jobsOrder = append(jobsOrder, j.ID)
	pruneJobsLocked()
	jobsMu.Unlock()

	go j.wait(ctx, c)
	return j, nil
}

// pruneJobsLocked drops the oldest finished jobs beyond maxJobs.
func pruneJobsLocked() {
	for len(jobsOrder) > maxJobs {
		id := jobsOrder[0]
		if jobsByID[id].isRunning() {
			return
		}
		delete(jobsByID, id)
		jobsOrder = jobsOrder[1:]
	}
}

func (j *job) isRunning() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status == jobRunning
}

// wait blocks until the command exits and records the outcome. A failed or
// cancelled dump never leaves a partial file or directory behind.
func (j *job) wait(ctx context.Context, c *exec.Cmd) {
	err := c.Wait()
	j.cancel()

	j.mu.Lock()
	switch {
	case err == nil:
		j.status = jobDone
	case j.status == jobCancelled:
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		j.status, j.errMsg = jobFailed, "timed out after "+backupTimeout.String()
	default:
		j.status, j.errMsg = jobFailed, err.Error()
	}
	failed := j.status != jobDone
	j.finished = time.Now()
	j.mu.Unlock()

	if failed {
		log.Printf("Backup job %s (%s) %s: %s", j.ID, j.Label, j.status, j.errMsg)
		if j.Out != "" {
			os.RemoveAll(j.Out)
		}
	}
}

// requestCancel marks a running job cancelled and kills its process.
func (j *job) requestCancel() {
	j.mu.Lock()
	if j.status == jobRunning {
		j.status = jobCancelled
	}
	j.mu.Unlock()
	j.cancel()
}

// jobView is the read-only snapshot templates render.
type jobView struct {
	ID, Label, Command, Status, Elapsed, Size, Output, Error string
	Running                                                  bool
}

func (j *job) view() jobView {
	j.mu.Lock()
	status, errMsg, finished := j.status, j.errMsg, j.finished
	j.mu.Unlock()

	end := finished
	if end.IsZero() {
		end = time.Now()
	}
	v := jobView{
		ID: j.ID, Label: j.Label, Command: j.Command, Status: status, Error: errMsg,
		Elapsed: end.Sub(j.Started).Round(time.Second).String(),
		Output:  j.out.String(), Running: status == jobRunning,
	}
	if j.Out != "" && status != jobFailed && status != jobCancelled {
		v.Size = humanSize(dirSize(j.Out))
	}
	return v
}

func lookupJob(w http.ResponseWriter, r *http.Request) (*job, bool) {
	jobsMu.Lock()
	j, ok := jobsByID[chi.URLParam(r, "id")]
	jobsMu.Unlock()
	if !ok {
		http.Error(w, "Unknown job (jobs are cleared when the server restarts)", http.StatusNotFound)
	}
	return j, ok
}

func (s *Server) handleJobStatus(w http.ResponseWriter, r *http.Request) {
	if j, ok := lookupJob(w, r); ok {
		RenderPartial(w, "backup_job.html", j.view())
	}
}

func (s *Server) handleJobCancel(w http.ResponseWriter, r *http.Request) {
	if j, ok := lookupJob(w, r); ok {
		j.requestCancel()
		RenderPartial(w, "backup_job.html", j.view())
	}
}

// handleJobList shows all known jobs, newest first.
func (s *Server) handleJobList(w http.ResponseWriter, r *http.Request) {
	jobsMu.Lock()
	all := make([]*job, 0, len(jobsOrder))
	for _, id := range jobsOrder {
		all = append(all, jobsByID[id])
	}
	jobsMu.Unlock()
	sort.SliceStable(all, func(a, b int) bool { return all[a].Started.After(all[b].Started) })

	views := make([]jobView, len(all))
	for i, j := range all {
		views[i] = j.view()
		views[i].Output = "" // the list needs no log tails
	}
	RenderPartial(w, "backup_jobs.html", map[string]any{"Jobs": views})
}
