package settings

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
)

type DownloadJobRecord struct {
	ID             string   `json:"id"`
	Hash           string   `json:"hash"`
	Title          string   `json:"title,omitempty"`
	TargetPath     string   `json:"target_path"`
	Files          []int    `json:"files,omitempty"`
	OutputPaths    []string `json:"output_paths,omitempty"`
	BytesTotal     int64    `json:"bytes_total"`
	BytesCompleted int64    `json:"bytes_completed"`
	Status         string   `json:"status"`
	PauseRequested bool     `json:"pause_requested,omitempty"`
	Error          string   `json:"error,omitempty"`
	CreatedAt      int64    `json:"created_at"`
	UpdatedAt      int64    `json:"updated_at"`
}

var downloadsMu sync.Mutex

const downloadsBucketRoot = "Torrents/Downloads"

func normalizeHash(hash string) string {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if hash == "" {
		return "_orphans"
	}
	return hash
}

func downloadsXPath(hash string) string {
	if hash == "" {
		return downloadsBucketRoot
	}
	return downloadsBucketRoot + "/" + hash
}

func SaveDownloadJob(job *DownloadJobRecord) {
	if job == nil || job.ID == "" {
		return
	}
	downloadsMu.Lock()
	defer downloadsMu.Unlock()
	saveDownloadJobLocked(job)
}

func saveDownloadJobLocked(job *DownloadJobRecord) {
	hash := normalizeHash(job.Hash)
	job.Hash = hash
	buf, err := json.Marshal(job)
	if err != nil {
		return
	}
	tdb.Set(downloadsXPath(hash), job.ID, buf)
}

func RemoveDownloadJob(id string) {
	if id == "" {
		return
	}
	downloadsMu.Lock()
	defer downloadsMu.Unlock()
	removeDownloadJobLocked(id)
}

func removeDownloadJobLocked(id string) bool {
	if id == "" {
		return false
	}
	hashes := tdb.List(downloadsBucketRoot)
	for _, hash := range hashes {
		bucket := downloadsXPath(hash)
		buf := tdb.Get(bucket, id)
		if len(buf) == 0 {
			continue
		}
		tdb.Rem(bucket, id)
		return true
	}
	return false
}

func GetDownloadJob(id string) *DownloadJobRecord {
	if id == "" {
		return nil
	}
	downloadsMu.Lock()
	defer downloadsMu.Unlock()
	return getDownloadJobLocked(id)
}

func getDownloadJobLocked(id string) *DownloadJobRecord {
	hashes := tdb.List(downloadsBucketRoot)
	for _, hash := range hashes {
		bucket := downloadsXPath(hash)
		buf := tdb.Get(bucket, id)
		if len(buf) == 0 {
			continue
		}
		var job DownloadJobRecord
		if err := json.Unmarshal(buf, &job); err != nil {
			continue
		}
		if job.Hash == "" {
			job.Hash = hash
		}
		return &job
	}
	return nil
}

func ListDownloadJobs() []*DownloadJobRecord {
	downloadsMu.Lock()
	defer downloadsMu.Unlock()
	j := listDownloadJobsLocked()
	sort.Slice(j, func(i, k int) bool {
		if j[i].CreatedAt == j[k].CreatedAt {
			return j[i].ID < j[k].ID
		}
		return j[i].CreatedAt > j[k].CreatedAt
	})
	return j
}

func listDownloadJobsLocked() []*DownloadJobRecord {
	hashes := tdb.List(downloadsBucketRoot)
	jobs := make([]*DownloadJobRecord, 0)
	for _, hash := range hashes {
		if hash == "" {
			continue
		}
		bucket := downloadsXPath(hash)
		ids := tdb.List(bucket)
		for _, id := range ids {
			buf := tdb.Get(bucket, id)
			if len(buf) == 0 {
				continue
			}
			var job DownloadJobRecord
			if err := json.Unmarshal(buf, &job); err != nil {
				continue
			}
			if job.Hash == "" {
				job.Hash = hash
			}
			jobs = append(jobs, &job)
		}
	}
	return jobs
}

func RemoveDownloadJobsByHash(hash string) {
	hash = normalizeHash(hash)
	if hash == "" {
		return
	}
	downloadsMu.Lock()
	defer downloadsMu.Unlock()
	bucket := downloadsXPath(hash)
	ids := tdb.List(bucket)
	for _, id := range ids {
		tdb.Rem(bucket, id)
	}
}
