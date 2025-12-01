package strm

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"server/log"
	"server/settings"
	"server/utils"
)

type FileMeta struct {
	ID     int
	Path   string
	Length int64
}

type JobMeta struct {
	JobID      string
	Hash       string
	Title      string
	Category   string
	TargetPath string
	Files      []FileMeta
}

var (
	managerOnce sync.Once
	manager     *Manager
)

func getManager() *Manager {
	managerOnce.Do(func() {
		manager = &Manager{}
	})
	return manager
}

type Manager struct{}

// cloneMeta creates a deep copy of JobMeta to avoid accidental mutations
// by callers while a sync or remove operation is in progress.
func cloneMeta(meta *JobMeta) *JobMeta {
	if meta == nil {
		return nil
	}
	copyMeta := *meta
	if len(meta.Files) > 0 {
		copyMeta.Files = append([]FileMeta(nil), meta.Files...)
	}
	return &copyMeta
}

func SyncJob(meta *JobMeta) {
	getManager().sync(cloneMeta(meta))
}

func RemoveJob(meta *JobMeta) {
	getManager().remove(cloneMeta(meta))
}

func (m *Manager) sync(meta *JobMeta) {
	if meta == nil || len(meta.Files) == 0 {
		return
	}
	sets := settings.BTsets
	if sets == nil {
		return
	}
	if !(sets.GenerateStrmFiles || sets.ForceGenerateStrmFiles) {
		return
	}
	root := strings.TrimSpace(sets.StreamPath)
	if root == "" {
		return
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		log.TLogln("strm: ensure root", err)
		return
	}
	baseURL := resolveBaseURL()
	for _, file := range meta.Files {
		if !isVideoFile(file.Path) {
			continue
		}
		m.writeStrmFile(meta, file, baseURL, sets.ForceGenerateStrmFiles)
	}
}

func isVideoFile(path string) bool {
	return utils.GetMimeType(path) == "video/*"
}

func (m *Manager) remove(meta *JobMeta) {
	if meta == nil {
		return
	}
	sets := settings.BTsets
	if sets == nil {
		return
	}
	root := strings.TrimSpace(sets.StreamPath)
	if root == "" {
		return
	}

	if meta.Title == "" {
		return
	}

	jobDir := utils.BuildMediaFolderName("stream", meta.Category, meta.Title)
	if jobDir == root || jobDir == "" {
		return
	}
	if err := os.RemoveAll(jobDir); err != nil {
		log.TLogln("strm: remove job", err)
	}
}

func (m *Manager) writeStrmFile(meta *JobMeta, file FileMeta, baseURL string, force bool) {
	if meta == nil {
		return
	}
	targetFile := FilePath(meta, file)
	if targetFile == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(targetFile), 0o755); err != nil {
		log.TLogln("strm: ensure dir", err)
		return
	}
	content, _ := BuildContent(meta, file, baseURL)
	if content == "" {
		m.removeFile(targetFile)
		return
	}
	if !force {
		if existing, err := os.ReadFile(targetFile); err == nil && string(existing) == content {
			return
		}
	}
	tmp := targetFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		log.TLogln("strm: write tmp", err)
		return
	}
	if err := os.Rename(tmp, targetFile); err != nil {
		log.TLogln("strm: commit", err)
		_ = os.Remove(tmp)
	}
}

func BuildContent(meta *JobMeta, file FileMeta, baseURL string) (string, bool) {
	if meta == nil {
		return "", false
	}
	localPath := localFilePath(meta, file)
	if isCompleteLocalFile(localPath, file.Length) {
		if localURL := localStreamURL(meta, file, baseURL); localURL != "" {
			return localURL, true
		}
	}
	if baseURL == "" {
		baseURL = resolveBaseURL()
	}
	return streamURL(baseURL, meta.Hash, file.ID, filepath.Base(file.Path)), false
}

func jobDirectory(meta *JobMeta) string {
	if meta == nil {
		return ""
	}
	root := streamRoot()
	if root == "" {
		return ""
	}
	jobDir := utils.BuildMediaFolderName("stream", meta.Category, meta.Title)
	return jobDir
}

func FilePath(meta *JobMeta, file FileMeta) string {
	if meta == nil {
		return ""
	}
	dir := jobDirectory(meta)
	if dir == "" {
		return ""
	}
	rel := sanitizeRelativePath(file.Path)
	if rel == "" {
		return ""
	}

	base := strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel)) + ".strm"
	subdir := filepath.Dir(rel)
	if subdir == "." || subdir == string(os.PathSeparator) {
		return filepath.Join(dir, base)
	}
	return filepath.Join(dir, subdir, base)
}

func streamRoot() string {
	sets := settings.BTsets
	if sets == nil {
		return ""
	}
	return strings.TrimSpace(sets.StreamPath)
}

func (m *Manager) removeFile(path string) {
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.TLogln("strm: remove file", err)
	}
}

func localFilePath(meta *JobMeta, file FileMeta) string {
	if meta == nil {
		return ""
	}
	return filepath.Join(meta.TargetPath, filepath.FromSlash(file.Path))
}

func isCompleteLocalFile(path string, expectedSize int64) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if !info.Mode().IsRegular() {
		return false
	}
	if expectedSize <= 0 {
		return true
	}
	return info.Size() >= expectedSize
}

func localStreamURL(meta *JobMeta, file FileMeta, baseURL string) string {
	if meta == nil || file.ID <= 0 {
		return ""
	}
	hash := strings.ToLower(strings.TrimSpace(meta.Hash))
	if hash == "" {
		return ""
	}
	root := strings.TrimSpace(meta.TargetPath)
	if root == "" {
		return ""
	}
	rel := sanitizeRelativePath(file.Path)
	if rel == "" {
		return ""
	}
	if baseURL == "" {
		baseURL = resolveBaseURL()
	}
	if baseURL == "" {
		return ""
	}
	rootToken := encodePathToken(root)
	relToken := encodePathToken(rel)
	safeBase := strings.TrimRight(baseURL, "/")
	if safeBase == "" {
		return ""
	}
	name := filepath.Base(rel)
	escapedName := url.PathEscape(name)
	return fmt.Sprintf("%s/stream/%s?link=%s&index=%d&play&localroot=%s&localrel=%s&localhash=%s",
		safeBase, escapedName, hash, file.ID, rootToken, relToken, hash)
}

func encodePathToken(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func streamURL(base, hash string, index int, name string) string {
	if hash == "" || index <= 0 {
		return ""
	}
	safeBase := strings.TrimRight(base, "/")
	escapedName := url.PathEscape(name)
	return fmt.Sprintf("%s/stream/%s?link=%s&index=%d&play", safeBase, escapedName, strings.ToLower(hash), index)
}

func sanitizeRelativePath(rel string) string {
	clean := filepath.Clean(strings.TrimSpace(rel))
	clean = strings.TrimPrefix(clean, "..")
	clean = strings.TrimPrefix(clean, string(os.PathSeparator))
	if clean == "." || clean == "" {
		return ""
	}
	clean = strings.ReplaceAll(clean, "\\", string(os.PathSeparator))
	clean = strings.TrimLeft(clean, string(os.PathSeparator))
	return clean
}

func resolveBaseURL() string {
	if env := strings.TrimSpace(os.Getenv("TS_STRM_HTTP_BASE")); env != "" {
		return strings.TrimRight(env, "/")
	}
	scheme := "http"
	host := strings.TrimSpace(settings.IP)
	port := strings.TrimSpace(settings.Port)
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	if settings.Ssl {
		scheme = "https"
		if settings.SslPort != "" {
			port = settings.SslPort
		}
	}
	if port == "" {
		if scheme == "https" {
			port = "8091"
		} else {
			port = "8090"
		}
	}
	return fmt.Sprintf("%s://%s:%s", scheme, host, port)
}
