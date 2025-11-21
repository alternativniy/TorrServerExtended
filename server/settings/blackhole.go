package settings

import "strings"

// BlackholeEntry describes a mapping between a blackhole filesystem path
// and a torrent hash stored in DB. It is stored in Settings bucket.
type BlackholeEntry struct {
	Path string
	Hash string
}

// key space: Settings / Blackhole:<path>

func blackholeKey(path string) (bucket, key string) {
	clean := strings.TrimSpace(path)
	return "Settings", "Blackhole:" + clean
}

func SaveBlackholeEntry(path, hash string) {
	if tdb == nil {
		return
	}
	path = strings.TrimSpace(path)
	hash = strings.TrimSpace(strings.ToLower(hash))
	if path == "" || hash == "" {
		return
	}
	bucket, key := blackholeKey(path)
	// store plain hash string
	tdb.Set(bucket, key, []byte(hash))
}

func RemoveBlackholeEntry(path string) {
	if tdb == nil {
		return
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	bucket, key := blackholeKey(path)
	tdb.Rem(bucket, key)
}

// ListBlackholeEntries returns map[path]hash.
func ListBlackholeEntries() map[string]string {
	res := make(map[string]string)
	if tdb == nil {
		return res
	}
	keys := tdb.List("Settings")
	for _, key := range keys {
		if !strings.HasPrefix(key, "Blackhole:") {
			continue
		}
		path := strings.TrimPrefix(key, "Blackhole:")
		if path == "" {
			continue
		}
		val := tdb.Get("Settings", key)
		if len(val) == 0 {
			continue
		}
		res[path] = strings.TrimSpace(strings.ToLower(string(val)))
	}
	return res
}
