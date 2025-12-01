package utils

import (
	"fmt"
	"os"
	"path/filepath"
	"server/settings"
	"strings"
)

// SanitizePathComponent makes a string safe to use as a single
// path component (folder or file name) by trimming spaces and
// replacing path separators with spaces.
func SanitizePathComponent(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "torrent"
	}
	name = strings.ReplaceAll(name, string(os.PathSeparator), " ")
	// Also guard against alternate separators on Windows-style paths.
	name = strings.ReplaceAll(name, "\\", " ")
	return strings.TrimSpace(name)
}

// ExtractYear tries to find a plausible release year (19xx or 20xx)
// in a title-like string. It ignores obvious false positives such as
// numbers immediately followed by "p" (e.g. 1080p) or by resolution
// markers.
func ExtractYear(s string) string {
	s = strings.ToLower(s)
	if !strings.Contains(s, "19") && !strings.Contains(s, "20") {
		return ""
	}
	for i := 0; i+4 <= len(s); i++ {
		sub := s[i : i+4]
		if sub < "1900" || sub > "2099" {
			continue
		}
		if i > 0 {
			prev := s[i-1]
			if (prev >= '0' && prev <= '9') || (prev >= 'a' && prev <= 'z') {
				continue
			}
		}
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

// NormalizeReleaseName does a lightweight cleanup of a torrent/filename:
// - strips extension
// - replaces dots/underscores with spaces
// - collapses multiple spaces
func NormalizeReleaseName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	ext := filepath.Ext(name)
	if ext != "" {
		name = strings.TrimSuffix(name, ext)
	}
	replacer := strings.NewReplacer(".", " ", "_", " ")
	name = replacer.Replace(name)
	fields := strings.Fields(name)
	return strings.Join(fields, " ")
}

// ParseTitleAndYearFromFilename tries to extract a human-friendly English title
// and release year from a .torrent/.magnet filename or release name.
// It removes common quality/source tags (1080p, WEB-DL, BluRay, x264, etc.)
// and uses ExtractYear to detect a plausible year.
func ParseTitleAndYearFromFilename(filename string) (string, string) {
	clean := NormalizeReleaseName(filename)
	if clean == "" {
		return "", ""
	}

	knownTrash := map[string]struct{}{
		"1080p": {}, "720p": {}, "2160p": {}, "480p": {}, "540p": {}, "576p": {},
		"4k": {}, "uhd": {},
		"web-dl": {}, "webrip": {}, "web": {},
		"bdrip": {}, "bdremux": {}, "brrip": {}, "bluray": {}, "bdrip-": {}, "hdrip": {}, "hdr": {},
		"hdtv": {}, "dvdrip": {}, "dvd": {},
		"x264": {}, "h264": {}, "x265": {}, "hevc": {}, "avc": {},
		"10bit": {}, "8bit": {},
		"remastered": {}, "extended": {}, "unrated": {}, "director's": {}, "directors": {},
		"cut": {}, "proper": {}, "repack": {},
		"russian": {}, "russian-": {}, "rus": {}, "eng": {}, "multi": {},
	}

	parts := strings.Fields(clean)
	if len(parts) == 0 {
		return "", ""
	}

	year := ExtractYear(clean)

	trimIdx := len(parts)
	for i := len(parts) - 1; i >= 0; i-- {
		p := strings.ToLower(parts[i])
		if p == year {
			trimIdx = i
			continue
		}
		if strings.HasPrefix(p, "[") || strings.HasSuffix(p, "]") {
			trimIdx = i
			continue
		}
		if _, ok := knownTrash[p]; ok {
			trimIdx = i
			continue
		}
		break
	}
	if trimIdx <= 0 {
		trimIdx = len(parts)
	}

	title := strings.TrimSpace(strings.Join(parts[:trimIdx], " "))
	if title == "" {
		return "", year
	}
	return title, year
}

// FolderTitleWithYear builds a folder-friendly title, optionally
// appending a detected year in parentheses when it looks reasonable.
func FolderTitleWithYear(baseTitle string) string {
	title, year := ParseTitleAndYearFromFilename(strings.TrimSpace(baseTitle))

	if year == "" {
		return title
	}
	if strings.Contains(title, year) {
		title = strings.ReplaceAll(title, year, "")
	}
	return strings.TrimSpace(fmt.Sprintf("%s (%s)", title, year))
}

func BuildMediaFolderName(jobType string, category string, title string) string {
	if title == "" {
		panic("title is empty")
	}

	base := ""

	switch jobType {
	case "stream":
		base = strings.TrimSpace(settings.BTsets.StreamPath)
	case "downloads":
		base = strings.TrimSpace(settings.BTsets.DownloadPath)
	default:
		// Use panic to make linters like staticcheck or govet complain about unreachable code
		panic(fmt.Sprintf("invalid jobType: %q (must be 'stream' or 'downloads')", jobType))
	}

	if base == "" {
		panic("base path is empty")
	}

	if category != "" {
		catFolder := settings.CategoryFolder(category)
		if catFolder != "" {
			base = filepath.Join(base, catFolder)
		}
	} else {
		base = filepath.Join(base, "uncategorized")
	}

	baseName := filepath.Base(title)
	ext := filepath.Ext(baseName)
	nameWithoutExt := strings.TrimSuffix(baseName, ext)
	if nameWithoutExt == "" {
		nameWithoutExt = SanitizePathComponent(baseName)
	}
	return filepath.Join(base, FolderTitleWithYear(nameWithoutExt))
}
