package settings

import (
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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
	DataPath          string
	StreamPath        string
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

	// Data
	GenerateStrmFiles      bool
	ForceGenerateStrmFiles bool
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

	normalizeBTSets(sets)

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
	ensureDataDirectories(BTsets)
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
	normalizeBTSets(sets)
	BTsets = sets
	ensureDataDirectories(BTsets)
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
			normalizeBTSets(BTsets)
			ensureDataDirectories(BTsets)
			return
		}
		log.TLogln("Error unmarshal btsets", err)
	}
	// initialize defaults on error
	SetDefaultConfig()
}

func normalizeBTSets(sets *BTSets) {
	if sets == nil {
		return
	}
	legacyDownload := strings.TrimSpace(sets.DownloadPath)
	migrated := strings.TrimSpace(sets.StreamPath) != "" || strings.TrimSpace(sets.DataPath) != ""
	dataRoot, downloadsRoot, streamRoot := resolveDataRoots(strings.TrimSpace(sets.DataPath), legacyDownload)
	sets.DataPath = dataRoot
	sets.DownloadPath = downloadsRoot
	sets.StreamPath = streamRoot
	sets.GenerateStrmFiles = normalizeBooleanFlag(sets.GenerateStrmFiles, migrated, "TS_GEN_STRM_FILES", true)
	sets.ForceGenerateStrmFiles = normalizeBooleanFlag(sets.ForceGenerateStrmFiles, migrated, "TS_FORCE_GEN_STRM_FILES", false)
	if sets.ForceGenerateStrmFiles {
		sets.GenerateStrmFiles = true
	}
}

func resolveDataRoots(requestedDataRoot, legacyDownload string) (string, string, string) {
	root := resolveDataPath(requestedDataRoot, legacyDownload)
	if root == "" {
		root = filepath.Join(Path, "data")
	}
	return root, filepath.Join(root, "downloads"), filepath.Join(root, "stream")
}

func resolveDataPath(requested, legacyDownload string) string {
	candidates := candidateDataDirs(strings.TrimSpace(requested), legacyDownload)
	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		if resolved, ok := ensureWritableDir(dir); ok {
			return resolved
		}
	}
	fallbacks := []string{
		filepath.Join(Path, "data"),
		defaultDataRoot(),
		filepath.Join(os.TempDir(), "torrserver-data"),
	}
	for _, dir := range fallbacks {
		if dir == "" {
			continue
		}
		if resolved, ok := ensureWritableDir(dir); ok {
			return resolved
		}
	}
	return filepath.Join(os.TempDir(), "torrserver-data")
}

func candidateDataDirs(requested, legacyDownload string) []string {
	list := make([]string, 0, 8)
	if requested != "" {
		list = append(list, filepath.Clean(requested))
	}
	if env := strings.TrimSpace(os.Getenv("TS_DATA_PATH")); env != "" {
		list = append(list, filepath.Clean(env))
	}
	if legacy := normalizeLegacyDataRoot(legacyDownload); legacy != "" {
		list = append(list, legacy)
	}
	if Path != "" {
		list = append(list, filepath.Join(Path, "data"))
	}
	if runtime.GOOS == "windows" {
		if home := strings.TrimSpace(os.Getenv("USERPROFILE")); home != "" {
			list = append(list, filepath.Join(home, "AppData", "Local", "TorrServer", "data"))
		}
	} else {
		list = append(list, "/opt/ts/data")
		if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
			list = append(list, filepath.Join(home, ".local", "share", "torrserver", "data"))
		}
	}
	return list
}

func normalizeLegacyDataRoot(legacy string) string {
	legacy = strings.TrimSpace(legacy)
	if legacy == "" {
		return ""
	}
	cleaned := filepath.Clean(legacy)
	base := strings.ToLower(filepath.Base(cleaned))
	if base == "downloads" {
		parent := filepath.Dir(cleaned)
		if parent != "" && parent != "." && parent != string(os.PathSeparator) {
			return parent
		}
	}
	return cleaned
}

func defaultDataRoot() string {
	if runtime.GOOS == "windows" {
		if home := strings.TrimSpace(os.Getenv("USERPROFILE")); home != "" {
			return filepath.Join(home, "AppData", "Local", "TorrServer", "data")
		}
		return filepath.Join(os.TempDir(), "torrserver-data")
	}
	return "/opt/ts/data"
}

func normalizeBooleanFlag(current bool, migrated bool, envKey string, def bool) bool {
	value := current
	if !migrated {
		value = def
	}
	if envVal, ok := lookupEnvBool(envKey); ok {
		value = envVal
	}
	return value
}

func lookupEnvBool(key string) (bool, bool) {
	val, ok := os.LookupEnv(key)
	if !ok {
		return false, false
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(val))
	if err != nil {
		return false, false
	}
	return parsed, true
}

func ensureWritableDir(dir string) (string, bool) {
	cleaned := filepath.Clean(dir)
	if cleaned == "" || cleaned == string(os.PathSeparator) || cleaned == "." {
		return "", false
	}
	if err := os.MkdirAll(cleaned, 0o755); err != nil {
		log.TLogln("resolveDataPath: mkdir failed", cleaned, err)
		return "", false
	}
	testFile := filepath.Join(cleaned, ".torrserver-write-test")
	if err := os.WriteFile(testFile, []byte("ok"), 0o644); err != nil {
		log.TLogln("resolveDataPath: write test failed", cleaned, err)
		return "", false
	}
	_ = os.Remove(testFile)
	return cleaned, true
}

func ensureDataDirectories(sets *BTSets) {
	if sets == nil {
		return
	}
	ensure := func(path string) {
		cleaned := strings.TrimSpace(path)
		if cleaned == "" {
			return
		}
		if err := os.MkdirAll(cleaned, 0o755); err != nil {
			log.TLogln("ensure data dir", cleaned, err)
		}
	}
	ensure(sets.DataPath)
	ensure(sets.DownloadPath)
	ensure(sets.StreamPath)
	for _, category := range CategoryFolders() {
		if sets.DownloadPath != "" {
			ensure(filepath.Join(sets.DownloadPath, category))
		}
		if sets.StreamPath != "" {
			ensure(filepath.Join(sets.StreamPath, category))
		}
	}
	// also ensure default torrents root structure for Sonarr/Radarr blackhole-like workflows
	torrentsRoot := strings.TrimSpace(os.Getenv("TS_TORR_DIR"))
	if torrentsRoot == "" {
		// by default, keep torrents root next to DataPath (dev-friendly)
		base := strings.TrimSpace(sets.DataPath)
		if base == "" {
			base = filepath.Join(Path, "data")
		}
		parent := filepath.Dir(base)
		if parent == "" || parent == "." || parent == string(os.PathSeparator) {
			parent = "."
		}
		torrentsRoot = filepath.Join(parent, "torrents")
	}
	ensure(torrentsRoot)
	for _, category := range CategoryFolders() {
		ensure(filepath.Join(torrentsRoot, "downloads", category))
		ensure(filepath.Join(torrentsRoot, "stream", category))
	}
}
