package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/core"
)

// maxUploadBytes caps a single attachment upload. Generous for a local,
// single-user tool (archives, datasets); the part still streams to disk.
const maxUploadBytes = 1 << 30 // 1 GiB

func (s *Server) registerAPI(mux *http.ServeMux) {
	mux.HandleFunc("/api/state", s.guard(s.handleState))
	mux.HandleFunc("/api/projects", s.guard(s.handleProjects))
	mux.HandleFunc("/api/projects/open", s.guard(s.handleProjectOpen))
	mux.HandleFunc("/api/projects/use", s.guard(s.handleProjectUse))
	mux.HandleFunc("/api/threads", s.guard(s.handleThreads))
	mux.HandleFunc("/api/threads/open", s.guard(s.handleThreadOpen))
	mux.HandleFunc("/api/threads/new", s.guard(s.handleThreadNew))
	mux.HandleFunc("/api/threads/delete", s.guard(s.handleThreadDelete))
	mux.HandleFunc("/api/search", s.guard(s.handleSearch))
	mux.HandleFunc("/api/mode", s.guard(s.handleMode))
	mux.HandleFunc("/api/permissions/skip", s.guard(s.handleSkipPerms))
	mux.HandleFunc("/api/effort", s.guard(s.handleEffort))
	mux.HandleFunc("/api/model", s.guard(s.handleModel))
	mux.HandleFunc("/api/memory", s.guard(s.handleMemory))
	mux.HandleFunc("/api/theme", s.guard(s.handleTheme))
	mux.HandleFunc("/api/budget", s.guard(s.handleBudget))
	mux.HandleFunc("/api/export", s.guard(s.handleExport))
	mux.HandleFunc("/api/upload", s.guard(s.handleUpload))
}

// ---- DTOs -----------------------------------------------------------------

type projectDTO struct {
	Slug  string `json:"slug"`
	Name  string `json:"name"`
	Cwd   string `json:"cwd"`
	Mode  string `json:"mode"`
	Model string `json:"model"`
}

type threadSummaryDTO struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Updated   time.Time `json:"updated"`
	Count     int       `json:"count"`
	SessionID string    `json:"sessionId"`
}

type stateDTO struct {
	Project   *projectDTO       `json:"project"`
	Thread    *threadSummaryDTO `json:"thread"`
	Mode        string `json:"mode"`
	SkipPerms   bool   `json:"skipPerms"`
	Effort      string `json:"effort"`
	Model       string `json:"model"`
	ModelActual string `json:"modelActual"`
	CtxUsed     int             `json:"ctxUsed"`
	CtxWindow   int             `json:"ctxWindow"`
	Limits      []core.RateLimit `json:"limits"`
	Cost        float64         `json:"cost"`
	Theme     string            `json:"theme"`
	Budget    float64           `json:"budget"`
	Perm      struct {
		OK   bool   `json:"ok"`
		Addr string `json:"addr"`
	} `json:"perm"`
}

func projectToDTO(p *core.Project) *projectDTO {
	if p == nil {
		return nil
	}
	return &projectDTO{Slug: p.Slug(), Name: p.Name, Cwd: p.Cwd, Mode: p.Mode, Model: p.Model}
}

func threadSummary(t *core.Thread) *threadSummaryDTO {
	if t == nil {
		return nil
	}
	return &threadSummaryDTO{ID: t.ID, Title: t.Title, Updated: t.Updated, Count: len(t.Messages), SessionID: t.ClaudeSessionID}
}

func (s *Server) state() stateDTO {
	var d stateDTO
	d.Project = projectToDTO(s.app.CurrentProject())
	d.Thread = threadSummary(s.app.CurrentThread())
	d.Mode = s.app.Mode().String()
	d.SkipPerms = s.app.SkipPermissions()
	d.Effort = s.app.Effort()
	d.Model = s.app.Model()
	d.ModelActual = s.app.ModelActual()
	d.CtxUsed, d.CtxWindow = s.app.ContextInfo()
	d.Limits = s.app.Limits()
	d.Cost = s.app.Cost()
	cfg := s.app.Config()
	d.Theme = cfg.Theme
	d.Budget = cfg.BudgetWarnUSD
	addr, ok := s.app.PermissionInfo()
	d.Perm.OK = ok
	d.Perm.Addr = addr
	return d
}

// ---- helpers --------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

func badRequest(w http.ResponseWriter, msg string) { http.Error(w, msg, http.StatusBadRequest) }

// ---- handlers -------------------------------------------------------------

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.state())
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.app.ListProjects()
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	out := make([]projectDTO, 0, len(projects))
	for _, p := range projects {
		out = append(out, *projectToDTO(p))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleProjectOpen(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Slug string `json:"slug"`
	}
	if err := readJSON(r, &body); err != nil || body.Slug == "" {
		badRequest(w, "slug required")
		return
	}
	if _, err := s.app.OpenProject(body.Slug); err != nil {
		badRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.state())
}

func (s *Server) handleProjectUse(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Cwd  string `json:"cwd"`
		Mode string `json:"mode"` // optional: open the connected folder in this mode
	}
	if err := readJSON(r, &body); err != nil || body.Cwd == "" {
		badRequest(w, "cwd required")
		return
	}
	if _, err := s.app.UseCwd(body.Cwd); err != nil {
		badRequest(w, err.Error())
		return
	}
	// The web client opts folders into agent mode on connect so they are not
	// read-only; chat stays the safe default for everything else.
	if body.Mode != "" {
		s.app.SetMode(core.ParseMode(body.Mode))
	}
	writeJSON(w, http.StatusOK, s.state())
}

func (s *Server) handleThreads(w http.ResponseWriter, r *http.Request) {
	threads, err := s.app.ListThreads()
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	out := make([]threadSummaryDTO, 0, len(threads))
	for _, t := range threads {
		out = append(out, *threadSummary(t))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleThreadOpen(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &body); err != nil || body.ID == "" {
		badRequest(w, "id required")
		return
	}
	t, err := s.app.OpenThread(body.ID)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleThreadNew(w http.ResponseWriter, r *http.Request) {
	s.app.NewThread()
	writeJSON(w, http.StatusOK, s.state())
}

func (s *Server) handleThreadDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &body); err != nil || body.ID == "" {
		badRequest(w, "id required")
		return
	}
	if err := s.app.DeleteThread(body.ID); err != nil {
		badRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		badRequest(w, "q required")
		return
	}
	hits, err := s.app.Search(q)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, hits)
}

func (s *Server) handleMode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode string `json:"mode"`
	}
	if err := readJSON(r, &body); err != nil {
		badRequest(w, "mode required")
		return
	}
	warn := s.app.SetMode(core.ParseMode(body.Mode))
	writeJSON(w, http.StatusOK, map[string]string{"warning": warn, "mode": s.app.Mode().String()})
}

func (s *Server) handleSkipPerms(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Skip bool `json:"skip"`
	}
	if err := readJSON(r, &body); err != nil {
		badRequest(w, "skip required")
		return
	}
	warn := s.app.SetSkipPermissions(body.Skip)
	writeJSON(w, http.StatusOK, map[string]any{
		"warning":   warn,
		"skipPerms": s.app.SkipPermissions(),
		"mode":      s.app.Mode().String(),
	})
}

func (s *Server) handleModel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model string `json:"model"`
	}
	if err := readJSON(r, &body); err != nil {
		badRequest(w, "model required")
		return
	}
	if err := s.app.SetModel(body.Model); err != nil {
		badRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.state())
}

func (s *Server) handleEffort(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Effort string `json:"effort"`
	}
	if err := readJSON(r, &body); err != nil {
		badRequest(w, "effort required")
		return
	}
	if err := s.app.SetEffort(body.Effort); err != nil {
		badRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"effort": s.app.Effort()})
}

func (s *Server) handleMemory(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		content, err := s.app.ReadMemory()
		if err != nil {
			badRequest(w, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"content": content})
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := readJSON(r, &body); err != nil {
		badRequest(w, "content required")
		return
	}
	if err := s.app.WriteMemory(body.Content); err != nil {
		badRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleTheme(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &body); err != nil || body.Name == "" {
		badRequest(w, "name required")
		return
	}
	if err := s.app.SetTheme(body.Name); err != nil {
		badRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleBudget(w http.ResponseWriter, r *http.Request) {
	var body struct {
		USD float64 `json:"usd"`
	}
	if err := readJSON(r, &body); err != nil || body.USD < 0 {
		badRequest(w, "usd required")
		return
	}
	if err := s.app.SetBudget(body.USD); err != nil {
		badRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	_ = readJSON(r, &body)
	path := body.Path
	if path == "" {
		t := s.app.CurrentThread()
		if t == nil || len(t.Messages) == 0 {
			badRequest(w, "нечего экспортировать")
			return
		}
		path = core.DefaultExportPath(s.app.CurrentProject(), t)
	} else {
		path = core.ExpandPath(path)
	}
	if err := s.app.ExportCurrentThread(path); err != nil {
		badRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": path})
}

// handleUpload accepts a single file and returns an absolute path usable as a
// turn attachment. The part is streamed straight to disk (constant memory) so
// large archives upload without buffering. Files land in a per-run uploads dir.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	mr, err := r.MultipartReader()
	if err != nil {
		badRequest(w, "invalid multipart form")
		return
	}
	limit := int64(maxUploadBytes)
	if mb := s.app.Config().MaxUploadMB; mb > 0 {
		limit = int64(mb) << 20
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			badRequest(w, "invalid multipart form")
			return
		}
		if part.FormName() != "file" {
			part.Close()
			continue
		}
		name := filepath.Base(part.FileName())
		if name == "" || name == "." || name == "/" {
			name = "upload"
		}
		dir := filepath.Join(os.TempDir(), "claude-code-linux-ui-uploads")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			part.Close()
			badRequest(w, err.Error())
			return
		}
		dst := filepath.Join(dir, strconv.FormatInt(time.Now().UnixNano(), 36)+"-"+name)
		out, err := os.Create(dst)
		if err != nil {
			part.Close()
			badRequest(w, err.Error())
			return
		}
		_, copyErr := copyLimited(out, part, limit)
		out.Close()
		part.Close()
		if copyErr != nil {
			os.Remove(dst)
			if errors.Is(copyErr, errFileTooLarge) {
				badRequest(w, fmt.Sprintf("файл слишком большой (макс %d МБ)", limit>>20))
			} else {
				badRequest(w, copyErr.Error())
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"path": dst, "name": name})
		return
	}
	badRequest(w, "file field required")
}
