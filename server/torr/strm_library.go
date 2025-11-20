package torr

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"server/log"
	"server/settings"
	"server/torr/state"
	"server/torr/strm"
)

const libraryJobPrefix = "library"

// RestoreLibraryStrm recreates STRM files for all torrents stored in the DB.
func RestoreLibraryStrm() {
	if !shouldGenerateLibraryStrm() {
		return
	}
	for _, torr := range ListTorrentsDB() {
		syncLibraryStrm(torr)
	}
}

func syncLibraryStrm(tor *Torrent) {
	if tor == nil || !shouldGenerateLibraryStrm() {
		return
	}
	hash := strings.ToLower(tor.Hash().HexString())
	if hash == "" {
		return
	}
	removeLibraryStrm(hash)
	if meta := buildLibraryStrmMeta(tor); meta != nil {
		strm.SyncJob(meta)
	}
}

func removeLibraryStrm(hash string) {
	norm := strings.ToLower(strings.TrimSpace(hash))
	if norm == "" {
		return
	}
	if tor := findTorrentByHash(norm); tor != nil {
		if meta := buildLibraryStrmMeta(tor); meta != nil {
			strm.RemoveJob(meta)
			return
		}
	}
	legacy := &strm.JobMeta{
		JobID: libraryJobID(norm),
		Hash:  norm,
	}
	for _, category := range settings.CategoryFolders() {
		legacy.Category = category
		strm.RemoveJob(legacy)
	}
}

func shouldGenerateLibraryStrm() bool {
	sets := settings.BTsets
	return sets != nil && (sets.GenerateStrmFiles || sets.ForceGenerateStrmFiles)
}

func buildLibraryStrmMeta(tor *Torrent) *strm.JobMeta {
	if tor == nil {
		return nil
	}
	hash := strings.ToLower(tor.Hash().HexString())
	if hash == "" {
		return nil
	}
	stats := libraryFileStats(tor)
	if len(stats) == 0 {
		return nil
	}
	files := make([]strm.FileMeta, 0, len(stats))
	for _, st := range stats {
		if st == nil || st.Id <= 0 {
			continue
		}
		path := strings.TrimSpace(st.Path)
		if path == "" {
			continue
		}
		files = append(files, strm.FileMeta{ID: st.Id, Path: path, Length: st.Length})
	}
	if len(files) == 0 {
		return nil
	}
	target := defaultLibraryTargetPath(tor)
	return &strm.JobMeta{
		JobID:      libraryJobID(hash),
		Hash:       hash,
		Title:      "",
		Category:   tor.Category,
		TargetPath: target,
		Files:      files,
		FlatLayout: true,
	}
}

func libraryJobID(hash string) string {
	return libraryJobPrefix + "-" + hash
}

func defaultLibraryTargetPath(tor *Torrent) string {
	if tor == nil {
		return ""
	}
	target, err := resolveJobTargetPath("", tor)
	if err == nil {
		return target
	}
	base := strings.TrimSpace(settings.BTsets.DownloadPath)
	if base == "" {
		return ""
	}
	return filepath.Join(base, settings.CategoryFolder(tor.Category))
}

func libraryFileStats(tor *Torrent) []*state.TorrentFileStat {
	if tor == nil {
		return nil
	}
	if status := tor.Status(); status != nil && len(status.FileStats) > 0 {
		return status.FileStats
	}
	if stats := parseFileStatsFromData(tor.Data); len(stats) > 0 {
		return stats
	}
	if loaded := LoadTorrent(tor); loaded != nil {
		if status := loaded.Status(); status != nil && len(status.FileStats) > 0 {
			return status.FileStats
		}
	}
	return nil
}

func parseFileStatsFromData(data string) []*state.TorrentFileStat {
	if strings.TrimSpace(data) == "" {
		return nil
	}
	var payload tsFiles
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		log.TLogln("strm: decode torrent data", err)
		return nil
	}
	return payload.TorrServer.Files
}

func findTorrentByHash(hash string) *Torrent {
	if hash == "" {
		return nil
	}
	if tor := GetTorrent(hash); tor != nil {
		return tor
	}
	for _, stored := range ListTorrentsDB() {
		if strings.EqualFold(stored.Hash().HexString(), hash) {
			return stored
		}
	}
	return nil
}
