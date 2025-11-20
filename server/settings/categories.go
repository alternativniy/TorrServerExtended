package settings

import "strings"

var normalizedCategories = []string{
	"uncategorized",
	"movie",
	"series",
	"music",
	"other",
}

// CategoryFolder normalizes user-provided category names into
// a deterministic folder name reused across the server.
func CategoryFolder(category string) string {
	cat := strings.ToLower(strings.TrimSpace(category))
	switch cat {
	case "", "none", "uncategorized", "без категории", "uncategory":
		return "uncategorized"
	case "movie", "movies", "film", "films", "фильм", "фильмы":
		return "movie"
	case "series", "tv", "tvshow", "serial", "сериал", "сериалы":
		return "series"
	case "music", "музыка":
		return "music"
	case "other", "misc", "разное":
		return "other"
	default:
		return "other"
	}
}

// CategoryFolders returns the list of canonical category folder names
// that the server pre-creates under downloads and stream directories.
func CategoryFolders() []string {
	return append([]string(nil), normalizedCategories...)
}
