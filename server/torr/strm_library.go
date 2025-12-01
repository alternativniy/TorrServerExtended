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

func CreateOrUpdateStrmJobForTorrent(tor *Torrent) {
	if !settings.BTsets.GenerateStrmFiles && !settings.BTsets.ForceGenerateStrmFiles {
		return
	}

	if tor == nil {
		return
	}
	meta := buildLibraryStrmMeta(tor)
	if meta == nil {
		return
	}

	strm.SyncJob(meta)
}

func CreateOrUpdateStrmJobForTorrentByHash(hash string) {
	if !settings.BTsets.GenerateStrmFiles && !settings.BTsets.ForceGenerateStrmFiles {
		return
	}
	norm := strings.ToLower(strings.TrimSpace(hash))
	log.TLogln("strm: update strm library job for torrent hash:", norm)
	if norm == "" {
		return
	}
	tor := GetTorrent(norm)
	log.TLogln("strm: found torrent for strm library job:", tor != nil, "hash:", norm, len(tor.GetDownloads()))
	if tor == nil || (len(tor.GetDownloads()) > 0 && !settings.BTsets.ForceGenerateStrmFiles) {
		return
	}
	CreateOrUpdateStrmJobForTorrent(tor)
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

	title := tor.Title
	if title == "" {
		if tor.Torrent != nil {
			title = tor.Torrent.Name()
		}
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
		Title:      title,
		Category:   tor.Category,
		TargetPath: target,
		Files:      files,
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
