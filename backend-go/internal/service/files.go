package service

import (
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// SafeTransferFile resolves fileName inside dir, rejecting anything that is
// not a plain basename or escapes dir (absolute paths, "..", empty).
func SafeTransferFile(dir, fileName string) (string, bool) {
	if fileName == "" || fileName == "." || filepath.Base(fileName) != fileName {
		return "", false
	}
	joined := filepath.Join(dir, fileName)
	rel, err := filepath.Rel(dir, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", false
	}
	return joined, true
}

var (
	versionTimestampRe = regexp.MustCompile(`(\d{14})`)
	versionSemverRe    = regexp.MustCompile(`[vV]?(\d+(?:\.\d+){1,3})`)
)

// ParseVersionFromFilename extracts a version from a file name, matching the
// Python implementation: a 14-digit timestamp wins and is returned with a "v"
// prefix, then v?x.y(.z...), otherwise "".
func ParseVersionFromFilename(filename string) string {
	if m := versionTimestampRe.FindStringSubmatch(filename); m != nil {
		return "v" + m[1]
	}
	if m := versionSemverRe.FindStringSubmatch(filename); m != nil {
		return "v" + m[1]
	}
	return ""
}

type FileInfo struct {
	FileName   string  `json:"file_name"`
	SizeMB     float64 `json:"size_mb"`
	ModifiedAt string  `json:"modified_at"`
	MatchedApp *App    `json:"matched_app"`
}

// ListTransferFiles scans dir like the Python /files endpoint: a missing
// directory yields an empty list, regular files only (symlinks resolved),
// sorted by mtime desc, matched_app is the first app whose match_keyword is
// contained in the file name.
func ListTransferFiles(dir string, apps []App) ([]FileInfo, error) {
	result := make([]FileInfo, 0)
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return result, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	type item struct {
		name string
		info os.FileInfo
	}
	items := make([]item, 0, len(entries))
	for _, e := range entries {
		fi, err := os.Stat(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		if !fi.Mode().IsRegular() {
			continue
		}
		items = append(items, item{name: e.Name(), info: fi})
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].info.ModTime().After(items[j].info.ModTime())
	})

	for _, it := range items {
		var matched *App
		for i := range apps {
			if strings.Contains(it.name, apps[i].MatchKeyword) {
				app := apps[i]
				matched = &app
				break
			}
		}
		result = append(result, FileInfo{
			FileName:   it.name,
			SizeMB:     math.Round(float64(it.info.Size())/1048576*100) / 100,
			ModifiedAt: it.info.ModTime().Format("2006-01-02 15:04:05"),
			MatchedApp: matched,
		})
	}
	return result, nil
}
