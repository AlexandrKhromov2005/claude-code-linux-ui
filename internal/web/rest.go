package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/core"
)

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
	Project *projectDTO       `json:"project"`
	Thread  *threadSummaryDTO `json:"thread"`
	Mode    string            `json:"mode"`
	Cost    float64           `json:"cost"`
	Theme   string            `json:"theme"`
	Budget  float64           `json:"budget"`
	Perm    struct {
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
		Cwd string `json:"cwd"`
	}
	if err := readJSON(r, &body); err != nil || body.Cwd == "" {
		badRequest(w, "cwd required")
		return
	}
	if _, err := s.app.UseCwd(body.Cwd); err != nil {
		badRequest(w, err.Error())
		return
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
// turn attachment. Files land in a per-run uploads directory.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		badRequest(w, "invalid multipart form")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		badRequest(w, "file field required")
		return
	}
	defer file.Close()

	dir := filepath.Join(os.TempDir(), "claude-code-linux-ui-uploads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		badRequest(w, err.Error())
		return
	}
	name := filepath.Base(header.Filename)
	if name == "" || name == "." || name == "/" {
		name = "upload"
	}
	dst := filepath.Join(dir, strconv.FormatInt(time.Now().UnixNano(), 36)+"-"+name)
	out, err := os.Create(dst)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	if _, err := copyLimited(out, file, 64<<20); err != nil {
		out.Close()
		badRequest(w, err.Error())
		return
	}
	out.Close()
	writeJSON(w, http.StatusOK, map[string]string{"path": dst, "name": name})
}
