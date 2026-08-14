package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// A job is a long-running command — a fuzzing campaign, a build, a test sweep —
// that outlives the turn that started it.
//
// It exists because of how a turn works here. Every turn is one `claude -p`
// process, and everything that process started dies with it. Claude Code's own
// background shells are children of that process, so asking it to "run this in
// the background" buys nothing across turns: measured, a script launched that way
// stopped the moment the turn ended, a few seconds in. Anything longer than a
// turn therefore has to be owned by something longer-lived, and the only thing
// here that qualifies is this server.
//
// So a job is deliberately detached twice over. It runs in its own session
// (setsid), so signals aimed at the server's process group never reach it, and it
// writes its own output and exit code straight to disk rather than through a pipe
// the server holds. Both together mean a job survives not just the turn but the
// server restarting underneath it: on startup the supervisor finds the record,
// checks whether the process is still there, and picks the watch back up.

// JobStatus is where a job stands.
type JobStatus string

const (
	JobRunning JobStatus = "running"
	JobDone    JobStatus = "done"    // exited 0
	JobFailed  JobStatus = "failed"  // exited non-zero
	JobStopped JobStatus = "stopped" // killed on request
	JobLost    JobStatus = "lost"    // process gone, outcome unknown
)

// Terminal reports whether the job has finished, however it ended.
func (s JobStatus) Terminal() bool { return s != JobRunning }

// Job is one supervised command.
type Job struct {
	ID          string    `json:"id"`
	Command     string    `json:"command"`
	Description string    `json:"description"`
	Dir         string    `json:"dir"`
	ProjectSlug string    `json:"projectSlug"`
	ThreadID    string    `json:"threadId"` // the conversation to wake when it ends
	PID         int       `json:"pid"`
	Status      JobStatus `json:"status"`
	ExitCode    int       `json:"exitCode"`
	StartedAt   time.Time `json:"startedAt"`
	EndedAt     time.Time `json:"endedAt"`

	// Notified latches once the finish has been reported into the conversation,
	// so a server restart never re-announces a job that was already handled.
	Notified bool `json:"notified"`
}

// Elapsed is how long the job ran, or has been running.
func (j *Job) Elapsed(now time.Time) time.Duration {
	if j.StartedAt.IsZero() {
		return 0
	}
	end := now
	if j.Status.Terminal() && !j.EndedAt.IsZero() {
		end = j.EndedAt
	}
	if d := end.Sub(j.StartedAt); d > 0 {
		return d
	}
	return 0
}

// JobView is a job plus the live detail that is not worth persisting: how much
// it has written and when it last said anything. Silence is the proof of life a
// user actually reads — a fuzzer that has not written a line in ten minutes is
// worth looking at even though its process is still there.
type JobView struct {
	Job
	LastOutput   string `json:"lastOutput"`   // last non-empty line written
	LastOutputAt int64  `json:"lastOutputAt"` // unix ms of the last write, 0 if none
	OutputBytes  int64  `json:"outputBytes"`
	ElapsedMs    int64  `json:"elapsedMs"`
}

// jobRecordFile and jobLogFile name a job's two files on disk.
func jobRecordFile(dir, id string) string { return filepath.Join(dir, id+".json") }
func jobLogFile(dir, id string) string    { return filepath.Join(dir, id+".log") }

// jobExitFile holds the exit status the job writes for itself. The supervisor
// cannot rely on wait(2): after a server restart it is no longer the parent, so
// the only trustworthy record of how a job ended is one the job leaves behind.
func jobExitFile(dir, id string) string { return filepath.Join(dir, id+".exit") }

// JobManager supervises every job for this installation.
type JobManager struct {
	dir  string
	poll time.Duration

	mu   sync.Mutex
	jobs map[string]*Job

	onChange func()    // a job's visible state changed
	onFinish func(Job) // a job reached a terminal state, exactly once

	stop chan struct{}
	once sync.Once
}

// NewJobManager builds a supervisor over a directory, loading any jobs already
// recorded there. Jobs still marked running are re-adopted: the process is
// checked, and one that is gone without leaving an exit status is recorded as
// lost rather than silently reported as finished.
func NewJobManager(dir string) (*JobManager, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	m := &JobManager{
		dir:  dir,
		poll: time.Second,
		jobs: map[string]*Job{},
		stop: make(chan struct{}),
	}
	m.loadExisting()
	return m, nil
}

// SetHooks registers the change and finish callbacks. onFinish is called at most
// once per job, from the watch goroutine.
func (m *JobManager) SetHooks(onChange func(), onFinish func(Job)) {
	m.mu.Lock()
	m.onChange = onChange
	m.onFinish = onFinish
	m.mu.Unlock()
}

// loadExisting reads persisted job records back in.
func (m *JobManager) loadExisting() {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(m.dir, e.Name()))
		if err != nil {
			continue
		}
		var j Job
		if json.Unmarshal(b, &j) != nil || j.ID == "" {
			continue
		}
		m.jobs[j.ID] = &j
	}
}

// StartSpec describes a job to launch.
type StartSpec struct {
	Command     string
	Description string
	Dir         string
	ProjectSlug string
	ThreadID    string
}

// maxCommandRunes bounds what is accepted as a command, so a runaway prompt
// cannot write an unbounded argv into a persisted record.
const maxCommandRunes = 16000

// Start launches a job and returns it. The command runs through `sh -c` in its
// own session, with output going straight to the job's log file.
func (m *JobManager) Start(spec StartSpec) (*Job, error) {
	cmdText := strings.TrimSpace(spec.Command)
	if cmdText == "" {
		return nil, errors.New("пустая команда")
	}
	if len([]rune(cmdText)) > maxCommandRunes {
		return nil, fmt.Errorf("команда длиннее %d символов", maxCommandRunes)
	}
	dir := spec.Dir
	if dir != "" {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("рабочая папка недоступна: %s", dir)
		}
	}

	id := newJobID()
	logPath := jobLogFile(m.dir, id)
	exitPath := jobExitFile(m.dir, id)

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	defer logFile.Close()

	// The exit status is written by the job itself rather than collected with
	// wait(2), because the supervisor may not be alive — or may no longer be the
	// parent — when the job ends.
	//
	// It is recorded from an EXIT trap rather than a trailing line, because a
	// trailing line only runs if control ever reaches it. Real commands end by
	// calling `exit` — `make || exit 1`, anything under `set -e` — which would
	// leave no status at all and make a perfectly ordinary failure look like a
	// job that vanished. The trap fires however the shell leaves, and the two
	// signal handlers exist so a terminated job also records how it went instead
	// of dying silently.
	quotedExit := shellQuote(exitPath)
	wrapped := fmt.Sprintf(
		"trap 'printf '\\''%%s'\\'' \"$?\" > %s' EXIT\ntrap 'exit 143' TERM\ntrap 'exit 130' INT\n%s\n",
		quotedExit, cmdText)

	cmd := exec.Command("/bin/sh", "-c", wrapped)
	cmd.Dir = dir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	// A new session detaches the job from the server's process group, so the job
	// is not taken down with the thing that happened to launch it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("не удалось запустить: %w", err)
	}
	// Release the child immediately: it is deliberately not ours to wait on, and
	// leaving a Wait outstanding would keep a zombie around for every job.
	go func() { _ = cmd.Wait() }()

	j := &Job{
		ID:          id,
		Command:     cmdText,
		Description: strings.TrimSpace(spec.Description),
		Dir:         dir,
		ProjectSlug: spec.ProjectSlug,
		ThreadID:    spec.ThreadID,
		PID:         cmd.Process.Pid,
		Status:      JobRunning,
		StartedAt:   time.Now(),
	}
	m.mu.Lock()
	m.jobs[id] = j
	m.mu.Unlock()
	m.persist(j)
	m.notifyChange()
	return j, nil
}

// Get returns a copy of one job.
func (m *JobManager) Get(id string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Job{}, false
	}
	return *j, true
}

// List returns every job, newest first.
func (m *JobManager) List() []JobView {
	m.mu.Lock()
	jobs := make([]Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		jobs = append(jobs, *j)
	}
	m.mu.Unlock()

	now := time.Now()
	out := make([]JobView, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, m.view(j, now))
	}
	sort.Slice(out, func(i, k int) bool { return out[i].StartedAt.After(out[k].StartedAt) })
	return out
}

// Running returns just the jobs still in flight, newest first.
func (m *JobManager) Running() []JobView {
	var out []JobView
	for _, v := range m.List() {
		if v.Status == JobRunning {
			out = append(out, v)
		}
	}
	return out
}

// view decorates a job with its live output detail.
func (m *JobManager) view(j Job, now time.Time) JobView {
	v := JobView{Job: j, ElapsedMs: j.Elapsed(now).Milliseconds()}
	if info, err := os.Stat(jobLogFile(m.dir, j.ID)); err == nil {
		v.OutputBytes = info.Size()
		v.LastOutputAt = info.ModTime().UnixMilli()
	}
	if line := lastLine(jobLogFile(m.dir, j.ID)); line != "" {
		v.LastOutput = line
	}
	return v
}

// Logs returns the last n lines of a job's output.
func (m *JobManager) Logs(id string, n int) (string, error) {
	m.mu.Lock()
	_, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("задача %s не найдена", id)
	}
	if n <= 0 {
		n = 50
	}
	return tailFile(jobLogFile(m.dir, id), n), nil
}

// Stop terminates a running job and everything it spawned. The whole process
// group is signalled, because the interesting case — a build, a fuzzer — is a
// tree of processes, and killing only the shell would orphan the rest.
func (m *JobManager) Stop(id string) error {
	m.mu.Lock()
	j, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("задача %s не найдена", id)
	}
	if j.Status.Terminal() {
		m.mu.Unlock()
		return nil // already over; stopping again is not an error
	}
	pid := j.PID
	m.mu.Unlock()

	if pid > 0 {
		if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("не удалось остановить: %w", err)
		}
		// Give it a moment to go down on its own before insisting.
		go func() {
			time.Sleep(5 * time.Second)
			if cur, ok := m.Get(id); ok && !cur.Status.Terminal() {
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		}()
	}
	m.finish(id, JobStopped, -1)
	return nil
}

// Watch polls running jobs until Close. Polling is the only option that works
// after a restart, when the supervisor is no longer the process's parent and
// wait(2) is unavailable to it.
func (m *JobManager) Watch() {
	t := time.NewTicker(m.poll)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.sweep()
		}
	}
}

// Close stops the watch loop. Jobs themselves are left running on purpose: they
// were detached so they could outlive this process.
func (m *JobManager) Close() { m.once.Do(func() { close(m.stop) }) }

// sweep checks every running job for an ending.
func (m *JobManager) sweep() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.jobs))
	for id, j := range m.jobs {
		if j.Status == JobRunning {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()

	for _, id := range ids {
		m.checkOne(id)
	}
	if len(ids) > 0 {
		m.notifyChange() // elapsed time and output move even when nothing ends
	}
}

// checkOne resolves one running job's fate.
func (m *JobManager) checkOne(id string) {
	// The exit file is the authority: it is written last, so seeing it means the
	// command is genuinely over and its status is known.
	if code, ok := readExitCode(jobExitFile(m.dir, id)); ok {
		status := JobDone
		if code != 0 {
			status = JobFailed
		}
		m.finish(id, status, code)
		return
	}
	m.mu.Lock()
	j, ok := m.jobs[id]
	pid := 0
	if ok {
		pid = j.PID
	}
	m.mu.Unlock()
	if !ok {
		return
	}
	if pid > 0 && processAlive(pid) {
		return
	}
	// The process is gone and left no status. Re-read the exit file once before
	// giving up: the two writes are not atomic with respect to each other, and a
	// job that ended between the checks above is finished, not lost.
	if code, ok := readExitCode(jobExitFile(m.dir, id)); ok {
		status := JobDone
		if code != 0 {
			status = JobFailed
		}
		m.finish(id, status, code)
		return
	}
	m.finish(id, JobLost, -1)
}

// finish moves a job to a terminal state exactly once and reports it.
func (m *JobManager) finish(id string, status JobStatus, code int) {
	m.mu.Lock()
	j, ok := m.jobs[id]
	if !ok || j.Status.Terminal() {
		m.mu.Unlock()
		return
	}
	j.Status = status
	j.ExitCode = code
	j.EndedAt = time.Now()
	snapshot := *j
	onFinish := m.onFinish
	m.mu.Unlock()

	m.persist(&snapshot)
	m.notifyChange()
	if onFinish != nil {
		onFinish(snapshot)
	}
}

// MarkNotified latches that a job's ending has been delivered into the
// conversation, so a restart does not announce it a second time.
func (m *JobManager) MarkNotified(id string) {
	m.mu.Lock()
	j, ok := m.jobs[id]
	if !ok || j.Notified {
		m.mu.Unlock()
		return
	}
	j.Notified = true
	snapshot := *j
	m.mu.Unlock()
	m.persist(&snapshot)
}

// AdoptOrphans resolves jobs that were left running when the server last exited.
// It runs once at startup, before Watch, so a job whose process died while
// nothing was watching is settled rather than shown as live forever.
func (m *JobManager) AdoptOrphans() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.jobs))
	for id, j := range m.jobs {
		if j.Status == JobRunning {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.checkOne(id)
	}
}

// Prune deletes finished jobs older than age, with their logs.
func (m *JobManager) Prune(age time.Duration) {
	cutoff := time.Now().Add(-age)
	m.mu.Lock()
	var gone []string
	for id, j := range m.jobs {
		if j.Status.Terminal() && !j.EndedAt.IsZero() && j.EndedAt.Before(cutoff) {
			gone = append(gone, id)
			delete(m.jobs, id)
		}
	}
	m.mu.Unlock()
	for _, id := range gone {
		_ = os.Remove(jobRecordFile(m.dir, id))
		_ = os.Remove(jobLogFile(m.dir, id))
		_ = os.Remove(jobExitFile(m.dir, id))
	}
	if len(gone) > 0 {
		m.notifyChange()
	}
}

func (m *JobManager) persist(j *Job) {
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return
	}
	_ = writeFileAtomic(jobRecordFile(m.dir, j.ID), b)
}

func (m *JobManager) notifyChange() {
	m.mu.Lock()
	fn := m.onChange
	m.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// ---- helpers ---------------------------------------------------------------

func newJobID() string {
	return "job-" + time.Now().UTC().Format("20060102T150405") + "-" + randHex(3)
}

// processAlive reports whether a pid is still there. Signal 0 performs the
// permission and existence checks without delivering anything.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// readExitCode reads the status a finished job left behind.
func readExitCode(path string) (int, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return 0, false // the file exists but the write has not landed yet
	}
	code, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return code, true
}

// shellQuote wraps a string as a single-quoted shell word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// tailBudget caps how much of a log file is read to answer a tail request, so a
// job that has written gigabytes is still cheap to inspect.
const tailBudget = 256 << 10

// tailFile returns roughly the last n lines of a file.
func tailFile(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	size := info.Size()
	start := int64(0)
	if size > tailBudget {
		start = size - tailBudget
	}
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && len(buf) > 0 {
		// A short read still leaves usable text; only a total failure is fatal.
		if !errors.Is(err, os.ErrClosed) && string(buf) == "" {
			return ""
		}
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if start > 0 && len(lines) > 0 {
		lines = lines[1:] // the first line was cut mid-way by the budget
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// lastLine returns the last non-empty line of a file, which is what a progress
// indicator shows.
func lastLine(path string) string {
	tail := tailFile(path, 40)
	if tail == "" {
		return ""
	}
	lines := strings.Split(tail, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			return s
		}
	}
	return ""
}
