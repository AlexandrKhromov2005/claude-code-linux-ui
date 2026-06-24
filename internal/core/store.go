package core

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

const appName = "claude-code-linux-ui"

// legacyAppName is the previous on-disk name; data is migrated once on startup.
const legacyAppName = "claude-tui"

// Config is the global user config (config.toml).
type Config struct {
	ClaudeBin     string  `toml:"claude_bin"`
	DefaultModel  string  `toml:"default_model"`
	DefaultMode   string  `toml:"default_mode"`
	Theme         string  `toml:"theme"`
	LastProject   string  `toml:"last_project"`
	BudgetWarnUSD float64 `toml:"budget_warn_usd"`  // 0 = off
	SkipPerms     bool    `toml:"skip_permissions"` // agent runs with --dangerously-skip-permissions
	Effort        string  `toml:"effort"`           // reasoning effort level ("" = model default)
	MaxUploadMB   int     `toml:"max_upload_mb"`    // attachment upload cap in MB (0 = built-in default)
}

// Permissions are the project's remembered allow/deny rules. Deny wins over allow.
type Permissions struct {
	Allow []string `toml:"allow"`
	Deny  []string `toml:"deny"`
}

// Project groups threads that share a working directory and context.
type Project struct {
	slug string // directory name, not serialized

	Name         string      `toml:"name"`
	Cwd          string      `toml:"cwd"`
	Model        string      `toml:"model"`
	Mode         string      `toml:"mode"`
	AllowedTools []string    `toml:"allowed_tools"`
	Permissions  Permissions `toml:"permissions"`
	Created      time.Time   `toml:"created"`
	Updated      time.Time   `toml:"updated"`
}

// Slug returns the on-disk identifier for the project.
func (p *Project) Slug() string { return p.slug }

// Msg is one persisted turn entry in a thread transcript.
type Msg struct {
	Role        string         `json:"role"` // user | assistant | tool | system
	Content     string         `json:"content"`
	Attachments []string       `json:"attachments,omitempty"`
	Ts          time.Time      `json:"ts"`
	ToolMeta    map[string]any `json:"tool_meta,omitempty"`
}

// Thread is one conversation: its transcript plus a link to the Claude session.
type Thread struct {
	ID              string    `json:"id"`
	Title           string    `json:"title"`
	Created         time.Time `json:"created"`
	Updated         time.Time `json:"updated"`
	ClaudeSessionID string    `json:"claude_session_id"`
	Messages        []Msg     `json:"messages"`
}

// Store maps the on-disk layout to typed reads and writes.
type Store struct {
	ConfigDir string
	DataDir   string
}

func configHome() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

func dataHome() string {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share")
}

// NewStore resolves the XDG paths, migrates the legacy claude-tui directories
// once, and ensures the base directories exist.
func NewStore() (*Store, error) {
	s := &Store{
		ConfigDir: filepath.Join(configHome(), appName),
		DataDir:   filepath.Join(dataHome(), appName),
	}
	migrateLegacyDir(filepath.Join(dataHome(), legacyAppName), s.DataDir)
	migrateLegacyDir(filepath.Join(configHome(), legacyAppName), s.ConfigDir)
	if err := os.MkdirAll(s.ConfigDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.projectsDir(), 0o755); err != nil {
		return nil, err
	}
	return s, nil
}

// migrateLegacyDir moves an old data/config directory to the new name, but only
// when the new location does not exist yet, so a real new dir is never clobbered.
func migrateLegacyDir(oldPath, newPath string) {
	if oldPath == newPath {
		return
	}
	if _, err := os.Stat(newPath); err == nil {
		return // new location already in use
	}
	if _, err := os.Stat(oldPath); err != nil {
		return // nothing to migrate
	}
	if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
		return
	}
	_ = os.Rename(oldPath, newPath)
}

func (s *Store) projectsDir() string           { return filepath.Join(s.DataDir, "projects") }
func (s *Store) projectDir(slug string) string { return filepath.Join(s.projectsDir(), slug) }
func (s *Store) threadsDir(slug string) string { return filepath.Join(s.projectDir(slug), "threads") }
func (s *Store) configPath() string            { return filepath.Join(s.ConfigDir, "config.toml") }

// MemoryPath returns the project's memory.md (fed to --append-system-prompt-file).
func (s *Store) MemoryPath(slug string) string { return filepath.Join(s.projectDir(slug), "memory.md") }

func (s *Store) threadPath(slug, id string) string {
	return filepath.Join(s.threadsDir(slug), id+".json")
}

// ---- config ---------------------------------------------------------------

func defaultConfig() Config {
	return Config{ClaudeBin: "claude", DefaultMode: "chat", Theme: "dark"}
}

// LoadConfig reads config.toml, returning sane defaults when it is absent.
func (s *Store) LoadConfig() (Config, error) {
	cfg := defaultConfig()
	b, err := os.ReadFile(s.configPath())
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := toml.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}
	if cfg.ClaudeBin == "" {
		cfg.ClaudeBin = "claude"
	}
	return cfg, nil
}

// SaveConfig writes config.toml atomically.
func (s *Store) SaveConfig(cfg Config) error {
	b, err := toml.Marshal(cfg)
	if err != nil {
		return err
	}
	return writeFileAtomic(s.configPath(), b)
}

// ---- projects -------------------------------------------------------------

var slugStrip = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(name string) string {
	s := slugStrip.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "project"
	}
	return s
}

func (s *Store) uniqueSlug(base string) string {
	slug := base
	for i := 2; ; i++ {
		if _, err := os.Stat(s.projectDir(slug)); errors.Is(err, os.ErrNotExist) {
			return slug
		}
		slug = fmt.Sprintf("%s-%d", base, i)
	}
}

// CreateProject makes a new project rooted at cwd and lays out its directories.
func (s *Store) CreateProject(name, cwd string) (*Project, error) {
	if strings.TrimSpace(name) == "" {
		name = filepath.Base(cwd)
	}
	now := time.Now()
	p := &Project{
		slug:         s.uniqueSlug(slugify(name)),
		Name:         name,
		Cwd:          cwd,
		Mode:         "chat",
		AllowedTools: []string{},
		Permissions:  Permissions{Allow: []string{}, Deny: []string{}},
		Created:      now,
		Updated:      now,
	}
	if err := os.MkdirAll(s.threadsDir(p.slug), 0o755); err != nil {
		return nil, err
	}
	if _, err := os.Stat(s.MemoryPath(p.slug)); errors.Is(err, os.ErrNotExist) {
		if err := writeFileAtomic(s.MemoryPath(p.slug), nil); err != nil {
			return nil, err
		}
	}
	if err := s.SaveProject(p); err != nil {
		return nil, err
	}
	return p, nil
}

// SaveProject writes project.toml atomically and bumps Updated.
func (s *Store) SaveProject(p *Project) error {
	p.Updated = time.Now()
	b, err := toml.Marshal(p)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(s.projectDir(p.slug), "project.toml"), b)
}

// LoadProject reads project.toml for the given slug.
func (s *Store) LoadProject(slug string) (*Project, error) {
	b, err := os.ReadFile(filepath.Join(s.projectDir(slug), "project.toml"))
	if err != nil {
		return nil, err
	}
	var p Project
	if err := toml.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	p.slug = slug
	if p.Mode == "" {
		p.Mode = "chat"
	}
	if p.Name == "" {
		p.Name = slug
	}
	return &p, nil
}

// ListProjects returns all projects, most recently updated first.
func (s *Store) ListProjects() ([]*Project, error) {
	entries, err := os.ReadDir(s.projectsDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Project
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p, err := s.LoadProject(e.Name())
		if err != nil {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

// ProjectForCwd finds an existing project whose working directory is cwd.
func (s *Store) ProjectForCwd(cwd string) (*Project, error) {
	projects, err := s.ListProjects()
	if err != nil {
		return nil, err
	}
	for _, p := range projects {
		if p.Cwd == cwd {
			return p, nil
		}
	}
	return nil, nil
}

// ReadMemory returns the project memory text ("" when the file is absent).
func (s *Store) ReadMemory(slug string) (string, error) {
	b, err := os.ReadFile(s.MemoryPath(slug))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

// WriteMemory replaces the project memory file.
func (s *Store) WriteMemory(slug, content string) error {
	return writeFileAtomic(s.MemoryPath(slug), []byte(content))
}

// ---- threads --------------------------------------------------------------

func newThreadID() string {
	return time.Now().UTC().Format("20060102T150405") + "-" + randHex(3)
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%06d", time.Now().UnixNano()%1_000_000)
	}
	return hex.EncodeToString(b)
}

// NewThread returns an unsaved, empty thread.
func (s *Store) NewThread() *Thread {
	now := time.Now()
	return &Thread{ID: newThreadID(), Created: now, Updated: now}
}

// SaveThread writes the transcript atomically and bumps Updated.
func (s *Store) SaveThread(slug string, t *Thread) error {
	if t.ID == "" {
		t.ID = newThreadID()
	}
	t.Updated = time.Now()
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.threadPath(slug, t.ID), b)
}

// LoadThread reads one thread transcript by id.
func (s *Store) LoadThread(slug, id string) (*Thread, error) {
	b, err := os.ReadFile(s.threadPath(slug, id))
	if err != nil {
		return nil, err
	}
	var t Thread
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// ListThreads returns the project's threads, most recently updated first.
func (s *Store) ListThreads(slug string) ([]*Thread, error) {
	entries, err := os.ReadDir(s.threadsDir(slug))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Thread
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		t, err := s.LoadThread(slug, strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

// DeleteThread removes a thread transcript.
func (s *Store) DeleteThread(slug, id string) error {
	return os.Remove(s.threadPath(slug, id))
}

// writeFileAtomic writes via a temp file and rename so readers never see a
// partial file.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmpName)
		if werr != nil {
			return werr
		}
		return cerr
	}
	return os.Rename(tmpName, path)
}
