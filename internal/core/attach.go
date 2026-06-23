package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".webp": true, ".bmp": true, ".svg": true,
}

// IsImagePath reports whether the path looks like an image by extension.
func IsImagePath(p string) bool {
	return imageExts[strings.ToLower(filepath.Ext(p))]
}

// ExpandPath resolves ~ and makes the path absolute.
func ExpandPath(p string) string {
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// ExpandDir expands ~ and makes the path absolute, requiring it to point at an
// existing directory. It returns the cleaned absolute path or an error a client
// can surface.
func ExpandDir(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("путь не указан")
	}
	p = ExpandPath(p)
	info, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("папка не найдена: %s", p)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("это файл, не директория: %s", p)
	}
	return p, nil
}

// ValidateAttachmentPath expands and checks an attachment path, returning the
// cleaned absolute path or an error a client can surface.
func ValidateAttachmentPath(p string) (string, error) {
	p = ExpandPath(p)
	info, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("файл не найден: %s", p)
	}
	if info.IsDir() {
		return "", fmt.Errorf("это директория, не файл: %s", p)
	}
	return p, nil
}

// RelDisplay shortens a path relative to a base directory when it sits inside it.
func RelDisplay(base, p string) string {
	if base != "" {
		if rel, err := filepath.Rel(base, p); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return filepath.Base(p)
}

// BuildPrompt appends attachments as @-references so Claude Code reads them.
func BuildPrompt(text string, attachments []string) string {
	var sb strings.Builder
	sb.WriteString(text)
	for _, a := range attachments {
		sb.WriteString(" @")
		sb.WriteString(a)
	}
	return sb.String()
}
