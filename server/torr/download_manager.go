package torr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/google/uuid"

	"server/log"
	"server/settings"
	"server/torr/state"
)

type DownloadStatus string

const (
	DownloadStatusPending  DownloadStatus = "pending"
	DownloadStatusRunning  DownloadStatus = "running"
	DownloadStatusPaused   DownloadStatus = "paused"
	DownloadStatusDone     DownloadStatus = "completed"
	DownloadStatusFailed   DownloadStatus = "failed"
	DownloadStatusCanceled DownloadStatus = "canceled"
)

type DownloadJob struct {
	ID             string
	Hash           string
	Title          string
	TargetPath     string
	Files          []int
	OutputPaths    []string
	BytesTotal     int64
	BytesCompleted int64
	Status         DownloadStatus
	Error          string
	CreatedAt      time.Time
	UpdatedAt      time.Time

	mu              sync.Mutex
	cancel          context.CancelFunc
	persistThrottle time.Time
	pauseRequested  bool
}

type downloadManager struct {
	mu          sync.Mutex
	jobs        map[string]*DownloadJob
	maxParallel int
	active      int
	slotCond    *sync.Cond
}

var (
	dlManagerOnce sync.Once
	dlManager     *downloadManager
)

func getDownloadManager() *downloadManager {
	dlManagerOnce.Do(func() {
		manager := &downloadManager{
			jobs:        make(map[string]*DownloadJob),
			maxParallel: settings.BTsets.MaxDownloadJobs,
		}
		manager.slotCond = sync.NewCond(&manager.mu)
		manager.restoreJobs()
		dlManager = manager
	})
	return dlManager
}

func (m *downloadManager) restoreJobs() {
	records := settings.ListDownloadJobs()
	for _, rec := range records {
		status := strings.ToLower(strings.TrimSpace(rec.Status))
		if status == "" {
			status = string(DownloadStatusPending)
		}
		job := &DownloadJob{
			ID:             rec.ID,
			Hash:           strings.ToLower(rec.Hash),
			Title:          rec.Title,
			TargetPath:     rec.TargetPath,
			Files:          append([]int(nil), rec.Files...),
			OutputPaths:    append([]string(nil), rec.OutputPaths...),
			BytesTotal:     rec.BytesTotal,
			BytesCompleted: rec.BytesCompleted,
			Status:         DownloadStatus(status),
			Error:          rec.Error,
			CreatedAt:      time.Unix(rec.CreatedAt, 0),
			UpdatedAt:      time.Unix(rec.UpdatedAt, 0),
		}
		job.pauseRequested = rec.PauseRequested

		if job.pauseRequested && job.Status != DownloadStatusPaused {
			job.Status = DownloadStatusPaused
			job.Error = ""
		}

		if job.Status == DownloadStatusRunning {
			job.Status = DownloadStatusPending
			job.Error = ""
		}

		m.jobs[job.ID] = job
		if job.Status == DownloadStatusPending {
			go m.tryStart(job)
		}
	}
}

func (m *downloadManager) persist(job *DownloadJob) {
	record := &settings.DownloadJobRecord{
		ID:             job.ID,
		Hash:           job.Hash,
		Title:          job.Title,
		TargetPath:     job.TargetPath,
		Files:          append([]int(nil), job.Files...),
		OutputPaths:    append([]string(nil), job.OutputPaths...),
		BytesTotal:     job.BytesTotal,
		BytesCompleted: job.BytesCompleted,
		Status:         string(job.Status),
		PauseRequested: job.isPauseRequested(),
		Error:          job.Error,
		CreatedAt:      job.CreatedAt.Unix(),
		UpdatedAt:      job.UpdatedAt.Unix(),
	}
	settings.SaveDownloadJob(record)
}

func (m *downloadManager) updateLimits() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.maxParallel = settings.BTsets.MaxDownloadJobs
	if m.maxParallel <= 0 {
		m.maxParallel = 1
	}
	m.slotCond.Broadcast()
}

func (m *downloadManager) enqueue(job *DownloadJob) {

	m.jobs[job.ID] = job
	m.persist(job)
	go m.tryStart(job)
}

func (m *downloadManager) tryStart(job *DownloadJob) {
	for {
		m.mu.Lock()
		if job.Status == DownloadStatusCanceled || job.Status == DownloadStatusDone || job.Status == DownloadStatusPaused {
			m.mu.Unlock()
			return
		}

		if m.active >= m.maxParallel {
			if job.Status != DownloadStatusPending {
				job.updateStatus(DownloadStatusPending, "")
				m.persist(job)
			}
			m.slotCond.Wait()
			m.mu.Unlock()
			continue
		}

		ctx, cancel := context.WithCancel(context.Background())
		job.mu.Lock()
		job.cancel = cancel
		job.mu.Unlock()

		m.active++
		job.updateStatus(DownloadStatusRunning, "")
		m.persist(job)
		m.mu.Unlock()

		err := m.executeJob(ctx, job)

		m.mu.Lock()
		job.mu.Lock()
		job.cancel = nil
		job.mu.Unlock()
		m.active--
		m.slotCond.Broadcast()

		if err != nil {
			if errors.Is(err, context.Canceled) {
				if job.isPauseRequested() {
					job.updateStatus(DownloadStatusPaused, "")
				} else if job.Status != DownloadStatusCanceled {
					job.updateStatus(DownloadStatusCanceled, "")
				}
			} else {
				job.updateStatus(DownloadStatusFailed, err.Error())
			}
		} else {
			job.updateStatus(DownloadStatusDone, "")
		}
		job.clearPauseRequest()
		m.persist(job)
		m.mu.Unlock()
		return
	}
}

func (m *downloadManager) executeJob(ctx context.Context, job *DownloadJob) error {
	torr := GetTorrent(job.Hash)
	if torr == nil {
		return fmt.Errorf("torrent %s not found", job.Hash)
	}
	if torr.Torrent == nil {
		loaded := LoadTorrent(torr)
		if loaded == nil {
			return fmt.Errorf("failed to load torrent %s", job.Hash)
		}
		torr = loaded
	}

	if !torr.GotInfo() {
		return errors.New("failed to retrieve torrent metadata")
	}

	if job.BytesTotal == 0 {
		total, err := job.calculateTotalSize(torr)
		if err != nil {
			return err
		}
		job.mu.Lock()
		job.BytesTotal = total
		job.UpdatedAt = time.Now()
		job.mu.Unlock()
		m.persist(job)
	}

	files := job.resolveFiles(torr)
	if len(files) == 0 {
		return errors.New("no files selected for download")
	}

	root := job.TargetPath
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	progress := job.scanDiskProgress(files, root)
	m.persist(job)

	for _, fileStat := range files {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		completed := progress[fileStat.Id]
		if completed >= fileStat.Length {
			continue
		}

		if err := m.downloadFile(ctx, torr, fileStat, root, job, completed); err != nil {
			return err
		}
		progress[fileStat.Id] = fileStat.Length
	}

	return nil
}

func (m *downloadManager) downloadFile(ctx context.Context, tor *Torrent, st *state.TorrentFileStat, root string, job *DownloadJob, completed int64) error {
	file := tor.findFileIndex(st.Id)
	if file == nil {
		return fmt.Errorf("file id %d not found", st.Id)
	}

	dstPath := filepath.Join(root, st.Path)
	if !strings.HasPrefix(filepath.Clean(dstPath), filepath.Clean(root)) {
		return fmt.Errorf("invalid path: %s", dstPath)
	}

	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return err
	}

	tmpPath := dstPath + ".part"
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()

	reader := tor.NewReader(file)
	if reader == nil {
		return errors.New("failed to create torrent reader")
	}
	defer tor.CloseReader(reader)

	if completed > 0 {
		if completed >= st.Length {
			return nil
		}
		if _, err := out.Seek(completed, io.SeekStart); err != nil {
			return err
		}
		if _, err := reader.Seek(completed, io.SeekStart); err != nil {
			return err
		}
	}

	buffer := make([]byte, 2<<20) // 2 MiB chunks

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		n, err := reader.Read(buffer)
		if n > 0 {
			if _, wErr := out.Write(buffer[:n]); wErr != nil {
				return wErr
			}
			if job.addProgress(int64(n)) {
				m.persist(job)
			}
		}

		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}

	if err := out.Close(); err != nil {
		return err
	}

	if err := os.Rename(tmpPath, dstPath); err != nil {
		return err
	}

	m.persist(job)

	return nil
}
func (m *downloadManager) cancelJob(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return false
	}
	if job.cancel != nil {
		job.cancel()
	}
	job.clearPauseRequest()
	job.updateStatus(DownloadStatusCanceled, "")
	m.persist(job)
	return true
}

func (m *downloadManager) removeJob(id string, deleteFiles bool) bool {
	m.mu.Lock()
	job, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return false
	}
	if job.Status != DownloadStatusDone && job.Status != DownloadStatusFailed && job.Status != DownloadStatusCanceled {
		job.updateStatus(DownloadStatusCanceled, "")
		m.persist(job)
	}
	delete(m.jobs, id)
	m.mu.Unlock()

	if deleteFiles && job != nil {
		if err := job.cleanupTargetPath(); err != nil {
			log.TLogln("download cleanup failed", err)
		}
	}
	settings.RemoveDownloadJob(id)
	return true
}

func (m *downloadManager) pauseJob(id string) bool {
	m.mu.Lock()
	job, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return false
	}
	switch job.Status {
	case DownloadStatusDone, DownloadStatusFailed, DownloadStatusCanceled:
		m.mu.Unlock()
		return false
	case DownloadStatusPaused:
		m.mu.Unlock()
		return true
	case DownloadStatusPending:
		job.updateStatus(DownloadStatusPaused, "")
		m.persist(job)
		m.slotCond.Broadcast()
		m.mu.Unlock()
		return true
	}
	job.requestPause()
	m.persist(job)
	cancel := job.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return true
}

func (m *downloadManager) resumeJob(id string) error {
	m.mu.Lock()
	job, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("job %s not found", id)
	}
	if job.Status != DownloadStatusPaused {
		m.mu.Unlock()
		return fmt.Errorf("job %s is not paused", id)
	}
	job.clearPauseRequest()
	job.updateStatus(DownloadStatusPending, "")
	m.persist(job)
	m.mu.Unlock()
	go m.tryStart(job)
	return nil
}

func (m *downloadManager) removeJobsByHash(hash string, deleteFiles bool) int {
	norm := strings.ToLower(strings.TrimSpace(hash))
	if norm == "" {
		return 0
	}
	m.mu.Lock()
	targets := make([]*DownloadJob, 0)
	for _, job := range m.jobs {
		if job.Hash == norm {
			targets = append(targets, job)
		}
	}
	m.mu.Unlock()
	count := 0
	for _, job := range targets {
		if job.Status == DownloadStatusRunning {
			m.cancelJob(job.ID)
		}
		if m.removeJob(job.ID, deleteFiles) {
			count++
		}
	}
	settings.RemoveDownloadJobsByHash(norm)
	return count
}

func (m *downloadManager) listJobs() []*DownloadJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := make([]*DownloadJob, 0, len(m.jobs))
	for _, job := range m.jobs {
		list = append(list, job.clone())
	}
	return list
}

func (m *downloadManager) getJob(id string) *DownloadJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	if job, ok := m.jobs[id]; ok {
		return job.clone()
	}
	return nil
}

func (job *DownloadJob) updateStatus(status DownloadStatus, errMsg string) {
	job.mu.Lock()
	defer job.mu.Unlock()
	job.Status = status
	job.Error = errMsg
	job.UpdatedAt = time.Now()
}

func (job *DownloadJob) addProgress(n int64) bool {
	job.mu.Lock()
	job.BytesCompleted += n
	now := time.Now()
	persist := now.Sub(job.persistThrottle) > time.Second || job.BytesCompleted >= job.BytesTotal
	if persist {
		job.persistThrottle = now
	}
	job.UpdatedAt = now
	job.mu.Unlock()
	return persist
}

func (job *DownloadJob) calculateTotalSize(tor *Torrent) (int64, error) {
	var total int64
	files := job.resolveFiles(tor)
	if len(files) == 0 {
		return 0, errors.New("no files selected")
	}
	for _, file := range files {
		total += file.Length
	}
	return total, nil
}

func (job *DownloadJob) resolveFiles(tor *Torrent) []*state.TorrentFileStat {
	status := tor.Status()
	if len(status.FileStats) == 0 {
		return nil
	}
	if len(job.Files) == 0 {
		return status.FileStats
	}
	sel := make(map[int]struct{}, len(job.Files))
	for _, id := range job.Files {
		sel[id] = struct{}{}
	}
	var files []*state.TorrentFileStat
	for _, file := range status.FileStats {
		if _, ok := sel[file.Id]; ok {
			files = append(files, file)
		}
	}
	return files
}

func (job *DownloadJob) scanDiskProgress(files []*state.TorrentFileStat, root string) map[int]int64 {
	progress := make(map[int]int64, len(files))
	var total int64
	for _, st := range files {
		completed := job.fileProgress(root, st)
		progress[st.Id] = completed
		total += completed
	}
	job.mu.Lock()
	if total > job.BytesCompleted {
		job.BytesCompleted = total
		job.UpdatedAt = time.Now()
	}
	job.mu.Unlock()
	return progress
}

func (job *DownloadJob) fileProgress(root string, st *state.TorrentFileStat) int64 {
	dstPath := filepath.Join(root, st.Path)
	maxLen := st.Length
	if info, err := os.Stat(dstPath); err == nil {
		if info.Size() >= maxLen {
			return maxLen
		}
	}
	if info, err := os.Stat(dstPath + ".part"); err == nil {
		completed := info.Size()
		if completed > maxLen {
			return maxLen
		}
		return completed
	}
	return 0
}

func (job *DownloadJob) requestPause() {
	job.mu.Lock()
	job.pauseRequested = true
	job.mu.Unlock()
}

func (job *DownloadJob) clearPauseRequest() {
	job.mu.Lock()
	job.pauseRequested = false
	job.mu.Unlock()
}

func (job *DownloadJob) isPauseRequested() bool {
	job.mu.Lock()
	defer job.mu.Unlock()
	return job.pauseRequested
}

func (job *DownloadJob) clone() *DownloadJob {
	job.mu.Lock()
	defer job.mu.Unlock()
	copyJob := &DownloadJob{
		ID:             job.ID,
		Hash:           job.Hash,
		Title:          job.Title,
		TargetPath:     job.TargetPath,
		Files:          append([]int(nil), job.Files...),
		OutputPaths:    append([]string(nil), job.OutputPaths...),
		BytesTotal:     job.BytesTotal,
		BytesCompleted: job.BytesCompleted,
		Status:         job.Status,
		Error:          job.Error,
		CreatedAt:      job.CreatedAt,
		UpdatedAt:      job.UpdatedAt,
	}
	return copyJob
}

func (job *DownloadJob) cleanupTargetPath() error {
	base := filepath.Clean(job.TargetPath)
	if base == "" || base == string(os.PathSeparator) || base == "." {
		return fmt.Errorf("refusing to remove unsafe path: %s", base)
	}
	paths := job.recordedOutputPaths()
	if len(paths) == 0 {
		return fmt.Errorf("no recorded download paths for job %s", job.ID)
	}
	var errs []string
	for _, rel := range paths {
		rel = strings.TrimSpace(rel)
		if rel == "" {
			continue
		}
		relPath := filepath.Clean(filepath.FromSlash(rel))
		full := filepath.Join(base, relPath)
		if full != base && !strings.HasPrefix(full, base+string(os.PathSeparator)) {
			continue
		}
		if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err.Error())
		}
		if err := os.Remove(full + ".part"); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err.Error())
		}
		cleanupEmptyParents(filepath.Dir(full), base)
	}
	if len(errs) > 0 {
		return fmt.Errorf("cleanup errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (job *DownloadJob) recordedOutputPaths() []string {
	job.mu.Lock()
	paths := append([]string(nil), job.OutputPaths...)
	job.mu.Unlock()
	if len(paths) > 0 {
		return paths
	}
	tor := GetTorrent(job.Hash)
	if tor == nil {
		return nil
	}
	if tor.Torrent == nil {
		if loaded := LoadTorrent(tor); loaded != nil {
			tor = loaded
		}
	}
	return collectOutputPathsFromTorrent(tor, job.Files)
}

func cleanupEmptyParents(current, base string) {
	base = filepath.Clean(base)
	if base == "" {
		return
	}
	prefix := base + string(os.PathSeparator)
	for current != base && strings.HasPrefix(current, prefix) {
		entries, err := os.ReadDir(current)
		if err != nil || len(entries) > 0 {
			break
		}
		if err := os.Remove(current); err != nil {
			break
		}
		current = filepath.Dir(current)
	}
}

func newDownloadJob(hash, title, target string, files []int, outputs []string) *DownloadJob {
	return &DownloadJob{
		ID:          uuid.NewString(),
		Hash:        strings.ToLower(hash),
		Title:       title,
		TargetPath:  target,
		Files:       append([]int(nil), files...),
		OutputPaths: append([]string(nil), outputs...),
		Status:      DownloadStatusPending,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
}

// Public API

func CreateDownloadJob(spec *torrent.TorrentSpec, title, poster, data, category string, files []int, targetPath string) (*DownloadJob, error) {
	if spec == nil {
		return nil, errors.New("invalid torrent spec")
	}

	tor, err := AddTorrent(spec, title, poster, data, category)
	if err != nil {
		return nil, err
	}

	return enqueueDownloadJob(tor, title, targetPath, files)
}

func CreateDownloadJobForTorrent(tor *Torrent, preferredTitle string, files []int, targetPath string) (*DownloadJob, error) {
	if tor == nil {
		return nil, errors.New("invalid torrent")
	}

	return enqueueDownloadJob(tor, preferredTitle, targetPath, files)
}

func enqueueDownloadJob(tor *Torrent, preferredTitle, targetPath string, files []int) (*DownloadJob, error) {
	if settings.ReadOnly {
		return nil, errors.New("download manager disabled in read-only mode")
	}

	if tor == nil {
		return nil, errors.New("invalid torrent")
	}

	if !tor.GotInfo() {
		return nil, errors.New("failed to fetch torrent metadata")
	}

	resolvedPath, err := resolveJobTargetPath(targetPath, tor)
	if err != nil {
		return nil, err
	}

	title := resolveDownloadTitle(tor, preferredTitle)
	hash := strings.ToLower(tor.Hash().HexString())
	if hash == "" {
		return nil, errors.New("invalid torrent hash")
	}

	outputs := collectOutputPathsFromTorrent(tor, files)
	job := newDownloadJob(hash, title, resolvedPath, files, outputs)

	manager := getDownloadManager()
	manager.enqueue(job)
	return job.clone(), nil
}

func resolveDownloadTitle(tor *Torrent, preferred string) string {
	if tor == nil {
		return fallbackTitle(preferred)
	}
	if tor.Title != "" {
		return tor.Title
	}
	if tor.Torrent != nil && tor.Torrent.Name() != "" {
		return tor.Torrent.Name()
	}
	if tor.TorrentSpec != nil && tor.TorrentSpec.DisplayName != "" {
		return tor.TorrentSpec.DisplayName
	}
	return fallbackTitle(preferred)
}

func fallbackTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "torrent"
	}
	return title
}

func resolveJobTargetPath(targetPath string, tor *Torrent) (string, error) {
	base := strings.TrimSpace(targetPath)
	if base == "" {
		base = settings.BTsets.DownloadPath
		if base == "" {
			return "", errors.New("download path is not configured")
		}
		if sub := categoryFolderName(tor); sub != "" {
			base = filepath.Join(base, sub)
		}
	}
	cleaned := filepath.Clean(base)
	if err := os.MkdirAll(cleaned, 0o755); err != nil {
		return "", err
	}
	return cleaned, nil
}

func categoryFolderName(tor *Torrent) string {
	var category string
	if tor != nil {
		category = tor.Category
	}
	return mapCategoryToFolder(category)
}

func mapCategoryToFolder(category string) string {
	cat := strings.ToLower(strings.TrimSpace(category))
	switch cat {
	case "", "none", "uncategorized", "без категории", "uncategory":
		return "uncategorized"
	case "movie", "movies", "film", "films", "фильм", "фильмы":
		return "movie"
	case "series", "tv", "tvshow", "serial", "сериал", "сериалы":
		return "series"
	case "music", "музыка":
		return "music"
	case "other", "misc", "разное":
		return "other"
	default:
		return "other"
	}
}

func collectOutputPathsFromTorrent(tor *Torrent, selected []int) []string {
	if tor == nil {
		return nil
	}
	status := tor.Status()
	if len(status.FileStats) == 0 {
		return nil
	}
	includeAll := len(selected) == 0
	var idFilter map[int]struct{}
	if !includeAll {
		idFilter = make(map[int]struct{}, len(selected))
		for _, id := range selected {
			idFilter[id] = struct{}{}
		}
	}
	paths := make([]string, 0, len(status.FileStats))
	for _, st := range status.FileStats {
		if !includeAll {
			if _, ok := idFilter[st.Id]; !ok {
				continue
			}
		}
		paths = append(paths, st.Path)
	}
	return paths
}

func ListDownloadJobs() []*DownloadJob {
	return getDownloadManager().listJobs()
}

func GetDownloadJob(id string) *DownloadJob {
	return getDownloadManager().getJob(id)
}

func CancelDownloadJob(id string) bool {
	return getDownloadManager().cancelJob(id)
}

func RemoveDownloadJob(id string, deleteFiles bool) bool {
	return getDownloadManager().removeJob(id, deleteFiles)
}

func PauseDownloadJob(id string) bool {
	return getDownloadManager().pauseJob(id)
}

func ResumeDownloadJob(id string) error {
	return getDownloadManager().resumeJob(id)
}

func RemoveDownloadJobsByHash(hash string, deleteFiles bool) int {
	return getDownloadManager().removeJobsByHash(hash, deleteFiles)
}

func ListDownloadStatusesByHash(hash string) []*state.TorrentDownloadStatus {
	return getDownloadManager().listStatusesByHash(hash)
}

func UpdateDownloadSettings() {
	getDownloadManager().updateLimits()
}

func (m *downloadManager) listStatusesByHash(hash string) []*state.TorrentDownloadStatus {
	if hash == "" {
		return nil
	}
	normHash := strings.ToLower(hash)
	m.mu.Lock()
	defer m.mu.Unlock()
	statuses := make([]*state.TorrentDownloadStatus, 0)
	for _, job := range m.jobs {
		if job.Hash == normHash {
			statuses = append(statuses, job.toTorrentDownloadStatus())
		}
	}
	sort.Slice(statuses, func(i, j int) bool {
		if statuses[i].UpdatedAt == statuses[j].UpdatedAt {
			return statuses[i].CreatedAt > statuses[j].CreatedAt
		}
		return statuses[i].UpdatedAt > statuses[j].UpdatedAt
	})
	return statuses
}

func (job *DownloadJob) toTorrentDownloadStatus() *state.TorrentDownloadStatus {
	job.mu.Lock()
	defer job.mu.Unlock()
	return &state.TorrentDownloadStatus{
		ID:             job.ID,
		Hash:           job.Hash,
		Title:          job.Title,
		Status:         string(job.Status),
		BytesTotal:     job.BytesTotal,
		BytesCompleted: job.BytesCompleted,
		TargetPath:     job.TargetPath,
		Error:          job.Error,
		CreatedAt:      job.CreatedAt.Unix(),
		UpdatedAt:      job.UpdatedAt.Unix(),
	}
}
