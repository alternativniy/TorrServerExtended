package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"server"
	"strconv"
	"strings"
	"time"

	"server/log"
	"server/settings"
	"server/torr"
	"server/utils"
	"server/version"
	apiutils "server/web/api/utils"

	"github.com/alexflint/go-arg"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/pkg/browser"

	_ "server/docs"
	_ "server/web/api"
)

type args struct {
	Port        string `arg:"-p" help:"web server port (default 8090)"`
	IP          string `arg:"-i" help:"web server addr (default empty)"`
	Ssl         bool   `help:"enables https"`
	SslPort     string `help:"web server ssl port, If not set, will be set to default 8091 or taken from db(if stored previously). Accepted if --ssl enabled."`
	SslCert     string `help:"path to ssl cert file. If not set, will be taken from db(if stored previously) or default self-signed certificate/key will be generated. Accepted if --ssl enabled."`
	SslKey      string `help:"path to ssl key file. If not set, will be taken from db(if stored previously) or default self-signed certificate/key will be generated. Accepted if --ssl enabled."`
	Path        string `arg:"-d" help:"database and config dir path"`
	LogPath     string `arg:"-l" help:"server log file path"`
	WebLogPath  string `arg:"-w" help:"web access log file path"`
	RDB         bool   `arg:"-r" help:"start in read-only DB mode"`
	HttpAuth    bool   `arg:"-a" help:"enable http auth on all requests"`
	DontKill    bool   `arg:"-k" help:"don't kill server on signal"`
	UI          bool   `arg:"-u" help:"open torrserver page in browser"`
	TorrentsDir string `arg:"-t" help:"autoload torrents from dir (root with downloads/stream subdirs)"`
	TorrentAddr string `help:"Torrent client address, like 127.0.0.1:1337 (default :PeersListenPort)"`
	PubIPv4     string `arg:"-4" help:"set public IPv4 addr"`
	PubIPv6     string `arg:"-6" help:"set public IPv6 addr"`
	SearchWA    bool   `arg:"-s" help:"search without auth"`
	MaxSize     string `arg:"-m" help:"max allowed stream size (in Bytes)"`
	TGToken     string `arg:"-T" help:"telegram bot token"`
}

func (args) Version() string {
	return "TorrServer " + version.Version
}

var params args

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())

	arg.MustParse(&params)

	if params.Path == "" {
		params.Path, _ = os.Getwd()
	}

	if params.Port == "" {
		params.Port = "8090"
	}

	settings.Path = params.Path
	settings.HttpAuth = params.HttpAuth
	log.Init(params.LogPath, params.WebLogPath)
	fmt.Println("=========== START ===========")
	fmt.Println("TorrServer", version.Version+",", runtime.Version()+",", "CPU Num:", runtime.NumCPU())
	if params.HttpAuth {
		log.TLogln("Use HTTP Auth file", settings.Path+"/accs.db")
	}
	if params.RDB {
		log.TLogln("Running in Read-only DB mode!")
	}
	dnsResolve()
	Preconfig(params.DontKill)

	if params.UI {
		go func() {
			time.Sleep(time.Second)
			if params.Ssl {
				browser.OpenURL("https://127.0.0.1:" + params.SslPort)
			} else {
				browser.OpenURL("http://127.0.0.1:" + params.Port)
			}
		}()
	}

	if params.TorrentAddr != "" {
		settings.TorAddr = params.TorrentAddr
	}

	if params.PubIPv4 != "" {
		settings.PubIPv4 = params.PubIPv4
	}

	if params.PubIPv6 != "" {
		settings.PubIPv6 = params.PubIPv6
	}

	go watchTDir(params.TorrentsDir)

	if params.MaxSize != "" {
		maxSize, err := strconv.ParseInt(params.MaxSize, 10, 64)
		if err == nil {
			settings.MaxSize = maxSize
		}
	}

	server.Start(params.Port, params.IP, params.SslPort, params.SslCert, params.SslKey, params.Ssl, params.RDB, params.SearchWA, params.TGToken)
	log.TLogln(server.WaitServer())
	log.Close()
	time.Sleep(time.Second * 3)
	os.Exit(0)
}

func dnsResolve() {
	addrs, err := net.LookupHost("www.google.com")
	if len(addrs) == 0 {
		log.TLogln("Check dns failed", addrs, err)

		fn := func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{}
			return d.DialContext(ctx, "udp", "1.1.1.1:53")
		}

		net.DefaultResolver = &net.Resolver{
			Dial: fn,
		}

		addrs, err = net.LookupHost("www.google.com")
		log.TLogln("Check cloudflare dns", addrs, err)
	} else {
		log.TLogln("Check dns OK", addrs, err)
	}
}

func watchTDir(root string) {
	time.Sleep(5 * time.Second)
	root = strings.TrimSpace(root)
	if root == "" {
		// prefer explicit TS_TORR_DIR if set
		if env := strings.TrimSpace(os.Getenv("TS_TORR_DIR")); env != "" {
			root = env
		} else {
			// fall back to the same dev-friendly logic as ensureDataDirectories:
			// keep torrents root next to DataPath / Path
			base := strings.TrimSpace(settings.BTsets.DataPath)
			if base == "" {
				base = filepath.Join(settings.Path, "data")
			}
			parent := filepath.Dir(base)
			if parent == "" || parent == "." || parent == string(os.PathSeparator) {
				parent = "."
			}
			root = filepath.Join(parent, "torrents")
		}
	}
	path, err := filepath.Abs(root)
	if err != nil {
		path = root
	}
	for {
		if _, err := os.ReadDir(path); err != nil {
			log.TLogln("Error read torrents root:", err)
			time.Sleep(5 * time.Second)
			continue
		}
		processTorrentsRoot(path)
		time.Sleep(5 * time.Second)
	}
}

func processTorrentsRoot(root string) {
	seen := make(map[string]string)
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			log.TLogln("Error walking torrents dir:", err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ".torrent") || strings.HasSuffix(lower, ".magnet") {
			// track presence for auto-removal logic (path -> hash)
			var hash string
			if strings.HasSuffix(lower, ".torrent") {
				hash = processTorrentFile(root, p)
			} else {
				hash = processMagnetFile(root, p)
			}
			if hash != "" {
				seen[p] = hash
				// Optionally delete source .torrent/.magnet file after successful processing
				if settings.BTsets != nil && settings.BTsets.BlackholeDeleteSourceFiles {
					if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
						log.TLogln("Error removing blackhole source file:", err)
					}
				}
			}
		}
		return nil
	})

	// Auto-remove torrents whose blackhole files disappeared
	existing := settings.ListBlackholeEntries()
	// update / add current ones
	for path, hash := range seen {
		settings.SaveBlackholeEntry(path, hash)
		delete(existing, path)
	}
	// anything left in existing has no corresponding file anymore
	for path, hash := range existing {
		if hash == "" {
			settings.RemoveBlackholeEntry(path)
			continue
		}
		log.TLogln("Blackhole file missing, auto-remove torrent", hash)
		deleteFiles := settings.BTsets != nil && settings.BTsets.BlackholeRemoveFiles
		torr.RemTorrent(hash, deleteFiles)
		settings.RemoveBlackholeEntry(path)
	}
}

func processTorrentFile(root, fullPath string) string {
	sp, err := openTorrentSpec(fullPath)
	if err != nil {
		log.TLogln("Error parse torrent file:", err)
		return ""
	}
	mode, category := inferModeAndCategory(root, fullPath)
	title := ""
	poster := ""
	data := ""
	// Try to extract a better English title and year from the source filename
	// and persist it in DB so all further jobs/STRM reuse the same value.
	baseName := filepath.Base(fullPath)
	if engTitle, year := utils.ParseTitleAndYearFromFilename(baseName); engTitle != "" {
		title = engTitle
		if year != "" && !strings.Contains(engTitle, year) {
			title = fmt.Sprintf("%s (%s)", engTitle, year)
		}
	}
	tor, err := torr.AddTorrent(sp, strings.TrimSpace(title), poster, data, category, mode == "download")
	if err != nil {
		log.TLogln("Error add torrent from file:", err)
		return ""
	}
	if !tor.GotInfo() {
		log.TLogln("Error get info from torrent")
		return ""
	}
	torr.SaveTorrentToDB(tor)
	hash := tor.Hash().HexString()
	return hash
}

func processMagnetFile(root, fullPath string) string {
	content, err := os.ReadFile(fullPath)
	if err != nil {
		log.TLogln("Error read magnet file:", err)
		return ""
	}
	link := strings.TrimSpace(string(content))
	if link == "" {
		return ""
	}
	mode, category := inferModeAndCategory(root, fullPath)
	title := ""
	poster := ""
	data := ""
	// Попробуем вытащить английский тайтл и год из имени .magnet файла
	// и сразу сохранить его в таком виде в DB.
	baseName := filepath.Base(fullPath)
	if engTitle, year := utils.ParseTitleAndYearFromFilename(baseName); engTitle != "" {
		title = engTitle
		if year != "" && !strings.Contains(engTitle, year) {
			title = fmt.Sprintf("%s (%s)", engTitle, year)
		}
	}
	// Reuse existing magnet parsing logic from web/api/utils
	sp, err := apiutils.ParseLink(link)
	if err != nil {
		log.TLogln("Error parse magnet link:", err)
		return ""
	}
	// Avoid creating duplicate torrents and jobs for the same magnet while metadata is pending.
	if existing := torr.GetTorrent(sp.InfoHash.HexString()); existing != nil {
		// Magnet already tracked; just return its hash without logging.
		return existing.Hash().HexString()
	}
	tor, err := torr.AddTorrent(sp, strings.TrimSpace(title), poster, data, category, mode == "download")
	if err != nil {
		log.TLogln("Error add magnet torrent:", err)
		return ""
	}
	// Ensure we have metadata before saving to DB to avoid panics
	if !tor.GotInfo() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for !tor.GotInfo() {
			if ctx.Err() != nil {
				log.TLogln("Timeout waiting for torrent info from magnet:", link)
				if tor != nil {
					tor.Drop()
				}
				return ""
			}
			time.Sleep(time.Second)
		}
	}
	torr.SaveTorrentToDB(tor)
	hash := tor.Hash().HexString()
	return hash
}

func inferModeAndCategory(root, fullPath string) (mode string, category string) {
	rel, err := filepath.Rel(root, fullPath)
	if err != nil {
		return "stream", "other"
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 2 {
		return "stream", "other"
	}
	switch strings.ToLower(parts[0]) {
	case "downloads":
		mode = "download"
	case "stream":
		mode = "stream"
	default:
		mode = "stream"
	}
	category = settings.CategoryFolder(parts[1])
	return
}

func openTorrentSpec(path string) (*torrent.TorrentSpec, error) {
	minfo, err := metainfo.LoadFromFile(path)
	if err != nil {
		return nil, err
	}
	info, err := minfo.UnmarshalInfo()
	if err != nil {
		return nil, err
	}

	// mag := minfo.Magnet(info.Name, minfo.HashInfoBytes())
	mag := minfo.Magnet(nil, &info)
	return &torrent.TorrentSpec{
		InfoBytes:   minfo.InfoBytes,
		Trackers:    [][]string{mag.Trackers},
		DisplayName: info.Name,
		InfoHash:    minfo.HashInfoBytes(),
	}, nil
}
