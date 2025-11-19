package settings

import (
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"server/log"
)

type BTSets struct {
	// Cache
	CacheSize       int64 // in byte, def 64 MB
	ReaderReadAHead int   // in percent, 5%-100%, [...S__X__E...] [S-E] not clean
	PreloadCache    int   // in percent

	// Disk
	UseDisk           bool
	TorrentsSavePath  string
	RemoveCacheOnDrop bool
	DownloadPath      string
	MaxDownloadJobs   int

	// Torrent
	ForceEncrypt             bool
	RetrackersMode           int  // 0 - don`t add, 1 - add retrackers (def), 2 - remove retrackers 3 - replace retrackers
	TorrentDisconnectTimeout int  // in seconds
	EnableDebug              bool // debug logs

	// DLNA
	EnableDLNA   bool
	FriendlyName string

	// Rutor
	EnableRutorSearch bool

	// BT Config
	EnableIPv6        bool
	DisableTCP        bool
	DisableUTP        bool
	DisableUPNP       bool
	DisableDHT        bool
	DisablePEX        bool
	DisableUpload     bool
	DownloadRateLimit int // in kb, 0 - inf
	UploadRateLimit   int // in kb, 0 - inf
	ConnectionsLimit  int
	PeersListenPort   int

	// HTTPS
	SslPort int
	SslCert string
	SslKey  string

	// Reader
	ResponsiveMode bool // enable Responsive reader (don't wait pieceComplete)
}

func (v *BTSets) String() string {
	buf, _ := json.Marshal(v)
	return string(buf)
}

var BTsets *BTSets

func SetBTSets(sets *BTSets) {
	if ReadOnly {
		return
	}
	// failsafe checks (use defaults)
	if sets.CacheSize == 0 {
		sets.CacheSize = 64 * 1024 * 1024
	}
	if sets.ConnectionsLimit == 0 {
		sets.ConnectionsLimit = 25
	}
	if sets.TorrentDisconnectTimeout == 0 {
		sets.TorrentDisconnectTimeout = 30
	}

	if sets.ReaderReadAHead < 5 {
		sets.ReaderReadAHead = 5
	}
	if sets.ReaderReadAHead > 100 {
		sets.ReaderReadAHead = 100
	}

	if sets.PreloadCache < 0 {
		sets.PreloadCache = 0
	}
	if sets.PreloadCache > 100 {
		sets.PreloadCache = 100
	}

	if sets.MaxDownloadJobs <= 0 {
		sets.MaxDownloadJobs = 3
	}

	sets.DownloadPath = resolveDownloadBasePath(sets.DownloadPath)

	if sets.TorrentsSavePath == "" {
		sets.UseDisk = false
	} else if sets.UseDisk {
		BTsets = sets

		go filepath.WalkDir(sets.TorrentsSavePath, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && strings.ToLower(d.Name()) == ".tsc" {
				BTsets.TorrentsSavePath = path
				log.TLogln("Find directory \"" + BTsets.TorrentsSavePath + "\", use as cache dir")
				return io.EOF
			}
			if d.IsDir() && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		})
	}

	BTsets = sets
	buf, err := json.Marshal(BTsets)
	if err != nil {
		log.TLogln("Error marshal btsets", err)
		return
	}
	tdb.Set("Settings", "BitTorr", buf)
}

func SetDefaultConfig() {
	sets := new(BTSets)
	sets.CacheSize = 64 * 1024 * 1024 // 64 MB
	sets.PreloadCache = 50
	sets.ConnectionsLimit = 25
	sets.RetrackersMode = 1
	sets.TorrentDisconnectTimeout = 30
	sets.ReaderReadAHead = 95 // 95%
	sets.MaxDownloadJobs = 3
	sets.DownloadPath = resolveDownloadBasePath("")
	BTsets = sets
	if !ReadOnly {
		buf, err := json.Marshal(BTsets)
		if err != nil {
			log.TLogln("Error marshal btsets", err)
			return
		}
		tdb.Set("Settings", "BitTorr", buf)
	}
}

func loadBTSets() {
	buf := tdb.Get("Settings", "BitTorr")
	if len(buf) > 0 {
		err := json.Unmarshal(buf, &BTsets)
		if err == nil {
			if BTsets.ReaderReadAHead < 5 {
				BTsets.ReaderReadAHead = 5
			}
			return
		}
		log.TLogln("Error unmarshal btsets", err)
	}
	// initialize defaults on error
	SetDefaultConfig()
}

func resolveDownloadBasePath(requested string) string {
	candidates := candidateDownloadDirs(strings.TrimSpace(requested))
	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		if resolved, ok := ensureWritableDir(dir); ok {
			return resolved
		}
	}
	fallback := filepath.Join(Path, "downloads")
	if resolved, ok := ensureWritableDir(fallback); ok {
		return resolved
	}
	if resolved, ok := ensureWritableDir(filepath.Join(os.TempDir(), "torrserver-downloads")); ok {
		return resolved
	}
	return fallback
}

func candidateDownloadDirs(requested string) []string {
	list := make([]string, 0, 6)
	if requested != "" {
		list = append(list, filepath.Clean(requested))
	}
	if env := strings.TrimSpace(os.Getenv("TORRSERVER_DOWNLOAD_PATH")); env != "" {
		list = append(list, filepath.Clean(env))
	}
	if runtime.GOOS == "windows" {
		if home := strings.TrimSpace(os.Getenv("USERPROFILE")); home != "" {
			list = append(list, filepath.Join(home, "Downloads", "TorrServer"))
		}
		if Path != "" {
			list = append(list, filepath.Join(Path, "downloads"))
		}
	} else {
		list = append(list, "/downloads")
		if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
			list = append(list, filepath.Join(home, "Downloads", "torrserver"))
		}
		if Path != "" {
			list = append(list, filepath.Join(Path, "downloads"))
		}
	}
	return list
}

func ensureWritableDir(dir string) (string, bool) {
	cleaned := filepath.Clean(dir)
	if cleaned == "" || cleaned == string(os.PathSeparator) || cleaned == "." {
		return "", false
	}
	if err := os.MkdirAll(cleaned, 0o755); err != nil {
		log.TLogln("resolveDownloadBasePath: mkdir failed", cleaned, err)
		return "", false
	}
	testFile := filepath.Join(cleaned, ".torrserver-write-test")
	if err := os.WriteFile(testFile, []byte("ok"), 0o644); err != nil {
		log.TLogln("resolveDownloadBasePath: write test failed", cleaned, err)
		return "", false
	}
	_ = os.Remove(testFile)
	return cleaned, true
}
