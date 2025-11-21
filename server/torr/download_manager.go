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
	"server/torr/strm"
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
	Category       string
	TargetPath     string
	Files          []int
	OutputPaths    []string
	FileMetas      []settings.DownloadFileMeta
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

// sanitizePathComponent makes a string safe to use as a single
// path component (folder or file name) by trimming spaces and
// replacing path separators with spaces.
func sanitizePathComponent(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "torrent"
	}
	name = strings.ReplaceAll(name, string(os.PathSeparator), " ")
	// Also guard against alternate separators on Windows-style paths.
	name = strings.ReplaceAll(name, "\\", " ")
	return strings.TrimSpace(name)
}

// extractYear tries to find a plausible release year (19xx or 20xx)
// in a title-like string. It ignores obvious false positives such as
// numbers immediately followed by "p" (e.g. 1080p) or by resolution
// markers.
func extractYear(s string) string {
	s = strings.ToLower(s)
	// Quick reject if there's no "19" or "20" at all.
	if !strings.Contains(s, "19") && !strings.Contains(s, "20") {
		return ""
	}
	// Simple scan for 4-digit numbers starting with 19 or 20.
	for i := 0; i+4 <= len(s); i++ {
		sub := s[i : i+4]
		if sub < "1900" || sub > "2099" {
			continue
		}
		// Check preceding char (if any) is not a letter or digit.
		if i > 0 {
			prev := s[i-1]
			if (prev >= '0' && prev <= '9') || (prev >= 'a' && prev <= 'z') {
				continue
			}
		}
		// Check following char to avoid 1080p/2160p style matches.
		if i+4 < len(s) {
			next := s[i+4]
			if next == 'p' || next == 'i' || (next >= '0' && next <= '9') {
				continue
			}
		}
		return sub
	}
	return ""
}

// folderTitleWithYear builds a folder-friendly title, optionally
// appending a detected year in parentheses when it looks reasonable.
func folderTitleWithYear(tor *Torrent, job *DownloadJob) string {
	base := strings.TrimSpace(job.Title)
	if base == "" && tor != nil {
		if tor.Torrent != nil && tor.Torrent.Info() != nil {
			base = strings.TrimSpace(tor.Torrent.Info().Name)
		}
		if base == "" && tor.TorrentSpec != nil {
			base = strings.TrimSpace(tor.TorrentSpec.DisplayName)
		}
	}
	if base == "" {
		return ""
	}

	// Try to find a year in base first, then fall back to torrent names.
	year := extractYear(base)
	if year == "" && tor != nil {
		if tor.Torrent != nil && tor.Torrent.Info() != nil {
			year = extractYear(tor.Torrent.Info().Name)
		}
		if year == "" && tor.TorrentSpec != nil {
			year = extractYear(tor.TorrentSpec.DisplayName)
		}
	}

	if year == "" {
		return base
	}
	// Если год уже явно присутствует в base (например, "(1994)"), не дублируем.
	if strings.Contains(base, year) {
		return base
	}
	return strings.TrimSpace(fmt.Sprintf("%s (%s)", base, year))
}

// buildMediaFolderName normalizes a human-friendly folder name for media content
// using job title (with optional year) when available and falling back to a
// filename without extension. The returned value is a *single* safe path
// component, never including a file extension.
func buildMediaFolderName(tor *Torrent, job *DownloadJob, fallbackPath string) string {
	cleanTitle := strings.TrimSpace(folderTitleWithYear(tor, job))
	if cleanTitle != "" {
		// Strip extension if someone accidentally passed a title with it.
		titleExt := filepath.Ext(cleanTitle)
		if titleExt != "" {
			cleanTitle = strings.TrimSuffix(cleanTitle, titleExt)
		}
		return sanitizePathComponent(cleanTitle)
	}
	// Fallback to basename without extension.
	baseName := filepath.Base(fallbackPath)
	ext := filepath.Ext(baseName)
	nameWithoutExt := strings.TrimSuffix(baseName, ext)
	if nameWithoutExt == "" {
		nameWithoutExt = baseName
	}
	return sanitizePathComponent(nameWithoutExt)
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

// kept for backward compatibility / potential future use
// func isMetadataError(err error) bool {
// 	if err == nil {
// 		return false
// 	}
// 	msg := strings.ToLower(err.Error())
// 	return strings.Contains(msg, "failed to retrieve torrent metadata")
// }

// withJobLock captures a snapshot of a job under its mutex and applies fn to it
// without holding the mutex during fn execution. This helps to avoid keeping
// locks while doing I/O or calling into other packages.
func withJobLock[T any](job *DownloadJob, fn func(*DownloadJob) T) T {
	if job == nil {
		var zero T
		return zero
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	return fn(job)
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
			Category:       rec.Category,
			TargetPath:     rec.TargetPath,
			Files:          append([]int(nil), rec.Files...),
			OutputPaths:    append([]string(nil), rec.OutputPaths...),
			FileMetas:      append([]settings.DownloadFileMeta(nil), rec.FileMetas...),
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
		go m.syncStrm(job)
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
		Category:       job.Category,
		TargetPath:     job.TargetPath,
		Files:          append([]int(nil), job.Files...),
		OutputPaths:    append([]string(nil), job.OutputPaths...),
		FileMetas:      append([]settings.DownloadFileMeta(nil), job.FileMetas...),
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
	// Acquire a slot respecting maxParallel limit.
	m.mu.Lock()
	for m.active >= m.maxParallel {
		m.slotCond.Wait()
	}
	m.active++
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		m.active--
		m.slotCond.Broadcast()
		m.mu.Unlock()
	}()

	// Check terminal states under job lock.
	st := withJobLock(job, func(j *DownloadJob) DownloadStatus { return j.Status })
	if st == DownloadStatusCanceled || st == DownloadStatusDone || st == DownloadStatusPaused {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	withJobLock(job, func(j *DownloadJob) struct{} {
		j.cancel = cancel
		j.Status = DownloadStatusRunning
		j.Error = ""
		j.UpdatedAt = time.Now()
		return struct{}{}
	})
	m.persist(job)

	m.handleStrmOnStart(job)

	err := m.executeJob(ctx, job)

	// Clear cancel and update status based on result.
	var completed bool
	withJobLock(job, func(j *DownloadJob) struct{} {
		j.cancel = nil
		if err != nil {
			if errors.Is(err, context.Canceled) {
				if j.pauseRequested {
					j.Status = DownloadStatusPaused
					j.Error = ""
				} else if j.Status != DownloadStatusCanceled {
					j.Status = DownloadStatusCanceled
					j.Error = ""
				}
			} else {
				j.Status = DownloadStatusFailed
				j.Error = err.Error()
			}
		} else {
			j.Status = DownloadStatusDone
			j.Error = ""
			completed = true
		}
		j.pauseRequested = false
		j.UpdatedAt = time.Now()
		return struct{}{}
	})
	m.persist(job)

	if completed {
		go m.syncStrm(job)
	}
}

func (m *downloadManager) executeJob(ctx context.Context, job *DownloadJob) error {
	// Resolve torrent (с лимитированными ретраями по реальным ошибкам),
	// а затем бесконечно ждём метаданные до cancel.
	const (
		loadRetryCount = 3
		loadRetryDelay = 5 * time.Second
		metaWaitStep   = time.Second
	)

	var torr *Torrent
	for attempt := 0; attempt < loadRetryCount; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		torr = GetTorrent(job.Hash)
		if torr == nil {
			time.Sleep(loadRetryDelay)
			continue
		}

		if torr.Torrent == nil {
			// Пытаемся лениво поднять торрент из DB-спека.
			if loaded := LoadTorrent(torr); loaded != nil {
				torr = loaded
				break
			}
			// Ошибка загрузки торрента – даём ещё пару попыток.
			time.Sleep(loadRetryDelay)
			continue
		}
		break
	}
	if torr == nil || torr.Torrent == nil {
		return fmt.Errorf("failed to load torrent %s", job.Hash)
	}

	// Теперь ждём GotInfo сколько потребуется, прерываясь только по ctx.
	for !torr.GotInfo() {
		if err := ctx.Err(); err != nil {
			return err
		}
		time.Sleep(metaWaitStep)
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
	restorePriority := m.boostPiecePriorities(torr, files)
	defer restorePriority()

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

	// Place downloads into a folder named after the job title (with optional year),
	// or fall back to the basename without extension. Folder name never contains a
	// file extension; the file keeps its original name with extension.
	baseName := filepath.Base(st.Path)
	folderName := buildMediaFolderName(tor, job, st.Path)
	fileName := baseName
	if fileName == "" || fileName == "." || fileName == string(os.PathSeparator) {
		fileName = folderName
	}
	dstPath := filepath.Join(root, folderName, fileName)
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

func (m *downloadManager) boostPiecePriorities(tor *Torrent, files []*state.TorrentFileStat) func() {
	if tor == nil || tor.Torrent == nil || tor.Torrent.Info() == nil || len(files) == 0 {
		return func() {}
	}
	btTorrent := tor.Torrent
	pieceLen := btTorrent.Info().PieceLength
	if pieceLen <= 0 {
		return func() {}
	}
	type pieceRange struct{ begin, end int }
	ranges := make([]pieceRange, 0, len(files))
	for _, st := range files {
		file := tor.findFileIndex(st.Id)
		if file == nil {
			continue
		}
		start := int(file.Offset() / pieceLen)
		end := int((file.Offset() + file.Length() + pieceLen - 1) / pieceLen)
		if start < 0 {
			start = 0
		}
		if end <= start {
			continue
		}
		ranges = append(ranges, pieceRange{begin: start, end: end})
	}
	if len(ranges) == 0 {
		return func() {}
	}
	for _, rng := range ranges {
		min := rng.begin
		max := rng.end
		btTorrent.DownloadPieces(min, max)
		for i := min; i < max; i++ {
			if piece := btTorrent.Piece(i); piece != nil {
				piece.SetPriority(torrent.PiecePriorityHigh)
			}
		}
	}
	return func() {
		if btTorrent == nil {
			return
		}
		for _, rng := range ranges {
			min := rng.begin
			max := rng.end
			btTorrent.CancelPieces(min, max)
		}
	}
}
func (m *downloadManager) cancelJob(id string) bool {
	m.mu.Lock()
	job, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return false
	}
	// make local copy of cancel to avoid holding manager lock during cancel
	var cancel context.CancelFunc
	withJobLock(job, func(j *DownloadJob) struct{} {
		cancel = j.cancel
		j.pauseRequested = false
		j.Status = DownloadStatusCanceled
		j.Error = ""
		j.UpdatedAt = time.Now()
		return struct{}{}
	})
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
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
	sets := settings.BTsets
	restoreJobStrm := sets != nil && sets.ForceGenerateStrmFiles
	restoreLibraryStrm := sets != nil && (sets.GenerateStrmFiles || sets.ForceGenerateStrmFiles)
	var metaWithFiles *strm.JobMeta
	if restoreJobStrm {
		if m.ensureFileMetas(job) {
			metaWithFiles = buildStrmMeta(job, true)
		} else {
			restoreJobStrm = false
		}
	}
	meta := metaWithFiles
	if meta == nil {
		meta = buildStrmMeta(job, false)
	}
	hash := strings.TrimSpace(job.Hash)
	withJobLock(job, func(j *DownloadJob) struct{} {
		if j.Status != DownloadStatusDone && j.Status != DownloadStatusFailed && j.Status != DownloadStatusCanceled {
			j.Status = DownloadStatusCanceled
			j.Error = ""
			j.pauseRequested = false
			j.UpdatedAt = time.Now()
		}
		return struct{}{}
	})
	delete(m.jobs, id)
	m.mu.Unlock()

	m.persist(job)
	if meta != nil {
		strm.RemoveJob(meta)
	}
	if restoreJobStrm && metaWithFiles != nil {
		go strm.SyncJob(metaWithFiles)
	}

	if deleteFiles && job != nil {
		if err := job.cleanupTargetPath(); err != nil {
			log.TLogln("download cleanup failed", err)
		}
	}
	if restoreLibraryStrm && hash != "" {
		go func(h string) {
			if tor := findTorrentByHash(h); tor != nil {
				syncLibraryStrm(tor)
			}
		}(hash)
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
	status := withJobLock(job, func(j *DownloadJob) DownloadStatus { return j.Status })
	switch status {
	case DownloadStatusDone, DownloadStatusFailed, DownloadStatusCanceled:
		m.mu.Unlock()
		return false
	case DownloadStatusPaused:
		m.mu.Unlock()
		return true
	}

	if status == DownloadStatusPending {
		withJobLock(job, func(j *DownloadJob) struct{} {
			j.Status = DownloadStatusPaused
			j.Error = ""
			j.pauseRequested = false
			j.UpdatedAt = time.Now()
			return struct{}{}
		})
		m.mu.Unlock()
		m.persist(job)
		m.mu.Lock()
		m.slotCond.Broadcast()
		m.mu.Unlock()
		return true
	}

	var cancel context.CancelFunc
	withJobLock(job, func(j *DownloadJob) struct{} {
		j.pauseRequested = true
		j.UpdatedAt = time.Now()
		cancel = j.cancel
		return struct{}{}
	})
	m.mu.Unlock()
	m.persist(job)
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
	isPaused := withJobLock(job, func(j *DownloadJob) bool {
		if j.Status != DownloadStatusPaused {
			return false
		}
		j.pauseRequested = false
		j.Status = DownloadStatusPending
		j.Error = ""
		j.UpdatedAt = time.Now()
		return true
	})
	m.mu.Unlock()
	if !isPaused {
		return fmt.Errorf("job %s is not paused", id)
	}
	m.persist(job)
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

// func (job *DownloadJob) updateStatus(status DownloadStatus, errMsg string) {
// 	withJobLock(job, func(j *DownloadJob) struct{} {
// 		j.Status = status
// 		j.Error = errMsg
// 		j.UpdatedAt = time.Now()
// 		return struct{}{}
// 	})
// }

func (job *DownloadJob) addProgress(n int64) bool {
	return withJobLock(job, func(j *DownloadJob) bool {
		j.BytesCompleted += n
		now := time.Now()
		persist := now.Sub(j.persistThrottle) > time.Second || (j.BytesTotal > 0 && j.BytesCompleted >= j.BytesTotal)
		if persist {
			j.persistThrottle = now
		}
		j.UpdatedAt = now
		return persist
	})
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
	withJobLock(job, func(j *DownloadJob) struct{} {
		if total > j.BytesCompleted {
			j.BytesCompleted = total
			j.UpdatedAt = time.Now()
		}
		return struct{}{}
	})
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

// func (job *DownloadJob) requestPause() {
// 	withJobLock(job, func(j *DownloadJob) struct{} {
// 		j.pauseRequested = true
// 		j.UpdatedAt = time.Now()
// 		return struct{}{}
// 	})
// }

// func (job *DownloadJob) clearPauseRequest() {
// 	withJobLock(job, func(j *DownloadJob) struct{} {
// 		j.pauseRequested = false
// 		j.UpdatedAt = time.Now()
// 		return struct{}{}
// 	})
// }

func (job *DownloadJob) isPauseRequested() bool {
	return withJobLock(job, func(j *DownloadJob) bool {
		return j.pauseRequested
	})
}

func (job *DownloadJob) clone() *DownloadJob {
	return withJobLock(job, func(j *DownloadJob) *DownloadJob {
		if j == nil {
			return nil
		}
		copyJob := &DownloadJob{
			ID:             j.ID,
			Hash:           j.Hash,
			Title:          j.Title,
			Category:       j.Category,
			TargetPath:     j.TargetPath,
			Files:          append([]int(nil), j.Files...),
			OutputPaths:    append([]string(nil), j.OutputPaths...),
			FileMetas:      append([]settings.DownloadFileMeta(nil), j.FileMetas...),
			BytesTotal:     j.BytesTotal,
			BytesCompleted: j.BytesCompleted,
			Status:         j.Status,
			Error:          j.Error,
			CreatedAt:      j.CreatedAt,
			UpdatedAt:      j.UpdatedAt,
		}
		return copyJob
	})
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
	var paths []string
	var metas []settings.DownloadFileMeta
	withJobLock(job, func(j *DownloadJob) struct{} {
		paths = append([]string(nil), j.OutputPaths...)
		metas = append([]settings.DownloadFileMeta(nil), j.FileMetas...)
		return struct{}{}
	})
	if len(paths) > 0 {
		return paths
	}
	if len(metas) > 0 {
		derived := make([]string, 0, len(metas))
		for _, meta := range metas {
			if meta.Path != "" {
				derived = append(derived, meta.Path)
			}
		}
		if len(derived) > 0 {
			return derived
		}
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

func newDownloadJob(hash, title, category, target string, files []int, outputs []string, metas []settings.DownloadFileMeta) *DownloadJob {
	return &DownloadJob{
		ID:          uuid.NewString(),
		Hash:        strings.ToLower(hash),
		Title:       title,
		Category:    category,
		TargetPath:  target,
		Files:       append([]int(nil), files...),
		OutputPaths: append([]string(nil), outputs...),
		FileMetas:   append([]settings.DownloadFileMeta(nil), metas...),
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
	fileMetas := collectFileMetasFromTorrent(tor, files)
	job := newDownloadJob(hash, title, tor.Category, resolvedPath, files, outputs, fileMetas)

	manager := getDownloadManager()
	manager.enqueue(job)
	go manager.syncStrm(job)
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
	return settings.CategoryFolder(category)
}

func (m *downloadManager) handleStrmOnStart(job *DownloadJob) {
	sets := settings.BTsets
	if sets == nil {
		return
	}
	if !(sets.GenerateStrmFiles || sets.ForceGenerateStrmFiles) {
		return
	}
	if sets.ForceGenerateStrmFiles {
		if meta := m.buildStrmMetaForSelectedFiles(job); meta != nil {
			go strm.SyncJob(meta)
		}
		return
	}
	if meta := m.buildStrmMetaForSelectedFiles(job); meta != nil {
		go strm.RemoveJob(meta)
	}
	if hash := strings.TrimSpace(job.Hash); hash != "" {
		go removeLibraryStrm(hash)
	}
}

func (m *downloadManager) buildStrmMetaForSelectedFiles(job *DownloadJob) *strm.JobMeta {
	if job == nil {
		return nil
	}
	hash := strings.TrimSpace(job.Hash)
	if hash == "" {
		return nil
	}
	tor := GetTorrent(hash)
	if tor == nil {
		tor = findTorrentByHash(hash)
	}
	if tor == nil {
		return nil
	}
	stats := libraryFileStats(tor)
	if len(stats) == 0 {
		return nil
	}
	selected := make(map[int]struct{})
	job.mu.Lock()
	files := append([]int(nil), job.Files...)
	job.mu.Unlock()
	for _, id := range files {
		selected[id] = struct{}{}
	}
	includeAll := len(selected) == 0
	meta := &strm.JobMeta{
		JobID:      job.ID,
		Hash:       hash,
		Title:      job.Title,
		Category:   job.Category,
		TargetPath: defaultLibraryTargetPath(tor),
		FlatLayout: true,
	}
	for _, st := range stats {
		if st == nil || st.Id <= 0 {
			continue
		}
		if !includeAll {
			if _, ok := selected[st.Id]; !ok {
				continue
			}
		}
		path := strings.TrimSpace(st.Path)
		if path == "" {
			continue
		}
		meta.Files = append(meta.Files, strm.FileMeta{ID: st.Id, Path: path, Length: st.Length})
	}
	if len(meta.Files) == 0 {
		return nil
	}
	return meta
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

func collectFileMetasFromTorrent(tor *Torrent, selected []int) []settings.DownloadFileMeta {
	if tor == nil {
		return nil
	}
	status := tor.Status()
	if status == nil || len(status.FileStats) == 0 {
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
	metas := make([]settings.DownloadFileMeta, 0, len(status.FileStats))
	for _, st := range status.FileStats {
		if !includeAll {
			if _, ok := idFilter[st.Id]; !ok {
				continue
			}
		}
		metas = append(metas, settings.DownloadFileMeta{
			ID:     st.Id,
			Path:   st.Path,
			Length: st.Length,
		})
	}
	return metas
}

func (m *downloadManager) syncStrm(job *DownloadJob) {
	if job == nil {
		return
	}
	sets := settings.BTsets
	if sets == nil {
		return
	}
	if !(sets.GenerateStrmFiles || sets.ForceGenerateStrmFiles) {
		return
	}
	if !m.ensureFileMetas(job) {
		return
	}
	files := m.filterFilesWithoutOtherJobs(job)
	if len(files) == 0 {
		return
	}
	meta := buildStrmMetaWithFiles(job, files)
	if meta == nil {
		return
	}
	strm.SyncJob(meta)
	if tor := GetTorrent(job.Hash); tor != nil {
		go syncLibraryStrm(tor)
	}
}

func (m *downloadManager) ensureFileMetas(job *DownloadJob) bool {
	has := withJobLock(job, func(j *DownloadJob) bool {
		return len(j.FileMetas) > 0
	})
	if has {
		return true
	}
	tor := GetTorrent(job.Hash)
	if tor == nil {
		return false
	}
	if tor.Torrent == nil {
		if loaded := LoadTorrent(tor); loaded != nil {
			tor = loaded
		}
	}
	if !tor.GotInfo() {
		return false
	}
	metas := collectFileMetasFromTorrent(tor, job.Files)
	if len(metas) == 0 {
		return false
	}
	withJobLock(job, func(j *DownloadJob) struct{} {
		j.FileMetas = append([]settings.DownloadFileMeta(nil), metas...)
		if j.Category == "" {
			j.Category = tor.Category
		}
		j.UpdatedAt = time.Now()
		return struct{}{}
	})
	m.persist(job)
	return true
}

func (m *downloadManager) filterFilesWithoutOtherJobs(job *DownloadJob) []settings.DownloadFileMeta {
	metas := withJobLock(job, func(j *DownloadJob) []settings.DownloadFileMeta {
		return append([]settings.DownloadFileMeta(nil), j.FileMetas...)
	})
	if len(metas) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	busy := make(map[int]struct{})
	for id, other := range m.jobs {
		if id == job.ID {
			continue
		}
		if other.Hash != job.Hash {
			continue
		}
		other.mu.Lock()
		files := append([]int(nil), other.Files...)
		other.mu.Unlock()
		if len(files) == 0 {
			busy[-1] = struct{}{}
			continue
		}
		for _, fid := range files {
			busy[fid] = struct{}{}
		}
	}
	res := make([]settings.DownloadFileMeta, 0, len(metas))
	for _, fm := range metas {
		if _, allBusy := busy[-1]; allBusy {
			continue
		}
		if _, used := busy[fm.ID]; used {
			continue
		}
		res = append(res, fm)
	}
	return res
}

func buildStrmMeta(job *DownloadJob, includeFiles bool) *strm.JobMeta {
	if job == nil {
		return nil
	}
	meta := &strm.JobMeta{
		JobID:      job.ID,
		Hash:       job.Hash,
		Title:      job.Title,
		Category:   job.Category,
		TargetPath: job.TargetPath,
		FlatLayout: true,
	}
	if !includeFiles {
		return meta
	}
	job.mu.Lock()
	files := append([]settings.DownloadFileMeta(nil), job.FileMetas...)
	job.mu.Unlock()
	if len(files) == 0 {
		return nil
	}
	meta.Files = make([]strm.FileMeta, 0, len(files))
	for _, f := range files {
		if f.ID <= 0 {
			continue
		}
		path := strings.TrimSpace(f.Path)
		if path == "" {
			continue
		}
		meta.Files = append(meta.Files, strm.FileMeta{ID: f.ID, Path: path, Length: f.Length})
	}
	if len(meta.Files) == 0 {
		return nil
	}
	return meta
}

func buildStrmMetaWithFiles(job *DownloadJob, files []settings.DownloadFileMeta) *strm.JobMeta {
	if job == nil || len(files) == 0 {
		return nil
	}
	meta := &strm.JobMeta{
		JobID:      job.ID,
		Hash:       job.Hash,
		Title:      job.Title,
		Category:   job.Category,
		TargetPath: job.TargetPath,
		FlatLayout: true,
	}
	meta.Files = make([]strm.FileMeta, 0, len(files))
	for _, f := range files {
		if f.ID <= 0 {
			continue
		}
		path := strings.TrimSpace(f.Path)
		if path == "" {
			continue
		}
		meta.Files = append(meta.Files, strm.FileMeta{ID: f.ID, Path: path, Length: f.Length})
	}
	if len(meta.Files) == 0 {
		return nil
	}
	return meta
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
	return withJobLock(job, func(j *DownloadJob) *state.TorrentDownloadStatus {
		if j == nil {
			return nil
		}
		return &state.TorrentDownloadStatus{
			ID:             j.ID,
			Hash:           j.Hash,
			Title:          j.Title,
			Status:         string(j.Status),
			BytesTotal:     j.BytesTotal,
			BytesCompleted: j.BytesCompleted,
			TargetPath:     j.TargetPath,
			Error:          j.Error,
			CreatedAt:      j.CreatedAt.Unix(),
			UpdatedAt:      j.UpdatedAt.Unix(),
		}
	})
}
