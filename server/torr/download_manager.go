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
	"server/utils"
	// "server/torr/strm"
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
	if job != nil {
		log.TLogln("download tryStart: job", job.ID, "hash", job.Hash, "target", job.TargetPath, "status", job.Status)
	}
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

	err := m.executeJob(ctx, job)

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
		}
		j.pauseRequested = false
		j.UpdatedAt = time.Now()
		return struct{}{}
	})
	m.persist(job)
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

	// Place downloads under job.TargetPath using the torrent's relative file path.
	relPath := filepath.Clean(filepath.FromSlash(st.Path))
	if relPath == "." || relPath == string(os.PathSeparator) || relPath == "" {
		return fmt.Errorf("invalid file path in torrent metadata: %q", st.Path)
	}
	dstPath := filepath.Join(root, relPath)
	if !strings.HasPrefix(filepath.Clean(dstPath), filepath.Clean(root)+string(os.PathSeparator)) && filepath.Clean(dstPath) != filepath.Clean(root) {
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

func (m *downloadManager) removeJob(id string, deleteFiles bool, fullRemove bool) bool {
	m.mu.Lock()
	job, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return false
	}

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

	// m.persist(job)

	if deleteFiles && job != nil {
		if err := job.cleanupTargetPath(); err != nil {
			log.TLogln("download cleanup failed", err)
		}
	}

	settings.RemoveDownloadJob(id)
	if !fullRemove {
		CreateOrUpdateStrmJobForTorrentByHash(job.Hash)
	}
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

func (m *downloadManager) removeJobsByHash(hash string, deleteFiles bool, fullRemove bool) int {
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
		if m.removeJob(job.ID, deleteFiles, fullRemove) {
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
		log.TLogln("download cleanup: unsafe base path, skipping", base)
		return fmt.Errorf("refusing to remove unsafe path: %s", base)
	}
	log.TLogln("download cleanup: removing folder", base)
	// delete everything under TargetPath, including the folder itself
	if err := os.RemoveAll(base); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.TLogln("download cleanup: RemoveAll failed", base, err)
		return fmt.Errorf("failed to remove download folder %s: %w", base, err)
	}
	log.TLogln("download cleanup: RemoveAll completed", base)
	return nil
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

func CreateDownloadJobForTorrent(tor *Torrent, preferredTitle string, files []int) (*DownloadJob, error) {
	if tor == nil {
		return nil, errors.New("invalid torrent")
	}

	if preferredTitle == "" {
		preferredTitle = tor.Title
	}

	if preferredTitle == "" {
		preferredTitle = tor.Name()
	}

	return enqueueDownloadJob(tor, preferredTitle, files)
}

func enqueueDownloadJob(tor *Torrent, preferredTitle string, files []int) (*DownloadJob, error) {
	if settings.ReadOnly {
		return nil, errors.New("download manager disabled in read-only mode")
	}

	if tor == nil {
		return nil, errors.New("invalid torrent")
	}

	if !tor.GotInfo() {
		return nil, errors.New("failed to fetch torrent metadata")
	}

	title := resolveDownloadTitle(tor, preferredTitle)
	resolvedPath, err := resolveJobTargetPath(title, tor)
	if err != nil {
		return nil, err
	}

	hash := strings.ToLower(tor.Hash().HexString())
	if hash == "" {
		return nil, errors.New("invalid torrent hash")
	}

	outputs := collectOutputPathsFromTorrent(tor, files)
	fileMetas := collectFileMetasFromTorrent(tor, files)
	job := newDownloadJob(hash, title, tor.Category, resolvedPath, files, outputs, fileMetas)

	manager := getDownloadManager()
	manager.enqueue(job)

	if !settings.BTsets.ForceGenerateStrmFiles {
		removeLibraryStrm(hash)
	}

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

func resolveJobTargetPath(title string, tor *Torrent) (string, error) {
	base := ""
	if title == "" && tor != nil {
		title = strings.TrimSpace(tor.Title)
	}
	if title != "" {
		base = utils.BuildMediaFolderName("downloads", tor.Category, title)
	} else {
		return "", errors.New("cannot resolve download title")
	}

	cleaned := filepath.Clean(base)
	if err := os.MkdirAll(cleaned, 0o755); err != nil {
		return "", err
	}
	return cleaned, nil
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

func ListDownloadJobs() []*DownloadJob {
	return getDownloadManager().listJobs()
}

func GetDownloadJob(id string) *DownloadJob {
	return getDownloadManager().getJob(id)
}

func CancelDownloadJob(id string) bool {
	return getDownloadManager().cancelJob(id)
}

func RemoveDownloadJob(id string, deleteFiles bool, fullRemove bool) bool {
	return getDownloadManager().removeJob(id, deleteFiles, fullRemove)
}

func PauseDownloadJob(id string) bool {
	return getDownloadManager().pauseJob(id)
}

func ResumeDownloadJob(id string) error {
	return getDownloadManager().resumeJob(id)
}

func RemoveDownloadJobsByHash(hash string, deleteFiles bool, fullRemove bool) int {
	return getDownloadManager().removeJobsByHash(hash, deleteFiles, fullRemove)
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
