package asset

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	url2 "net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/adrg/xdg"
	"github.com/muhammadmuzzammil1998/jsonc"
	"github.com/v2rayA/v2rayA/common"
	"github.com/v2rayA/v2rayA/common/files"
	"github.com/v2rayA/v2rayA/conf"
	"github.com/v2rayA/v2rayA/pkg/util/log"
)

const maxAssetDownloadSize int64 = 256 << 20

func readAssetBody(resp *http.Response) ([]byte, error) {
	if resp.ContentLength > maxAssetDownloadSize {
		return nil, fmt.Errorf("asset exceeds the 256 MiB download limit")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxAssetDownloadSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > maxAssetDownloadSize {
		return nil, fmt.Errorf("asset exceeds the 256 MiB download limit")
	}
	return b, nil
}

func GetV2rayLocationAssetOverride() string {
	if assetDir := conf.GetEnvironmentConfig().V2rayAssetsDirectory; assetDir != "" {
		return assetDir
	}
	if assetDir := os.Getenv("V2RAY_LOCATION_ASSET"); assetDir != "" {
		return assetDir
	}
	if runtime.GOOS != "windows" {
		return runtimeAssetDir()
	} else {
		return conf.GetEnvironmentConfig().Config
	}
}

// runtimeAssetDir is where the core's asset links go when nothing names a
// directory: v2raya's subdirectory of the XDG runtime directory. A service
// user has no session, so /run/user/<uid> does not exist and cannot be
// created by it; then the configuration directory, which the process owns,
// holds the links instead of the start failing on the lookup.
func runtimeAssetDir() string {
	dir := filepath.Join(xdg.RuntimeDir, "v2raya")
	if err := os.MkdirAll(dir, 0700); err == nil {
		return dir
	}
	return conf.GetEnvironmentConfig().Config
}

func GetV2rayLocationAsset(filename string) (string, error) {
	// All variants use XRAY_LOCATION_ASSET; dat files are stored under
	// v2raya's own XDG data subdirectory ("v2raya/"), not under "xray/".
	const envKey = "XRAY_LOCATION_ASSET"
	const folder = "v2raya"

	location := os.Getenv(envKey)
	// check if XRAY_LOCATION_ASSET is set
	if location != "" {
		// add XRAY_LOCATION_ASSET to search path
		searchPaths := []string{
			filepath.Join(location, filename),
		}
		// additional paths for non windows platforms
		if runtime.GOOS != "windows" {
			searchPaths = append(
				searchPaths,
				filepath.Join("/usr/local/share", folder, filename),
				filepath.Join("/usr/share", folder, filename),
			)
		}
		for _, searchPath := range searchPaths {
			if _, err := os.Stat(searchPath); err != nil && errors.Is(err, fs.ErrNotExist) {
				continue
			}
			// return the first path that exists
			return searchPath, nil
		}
		// or download asset into XRAY_LOCATION_ASSET
		return searchPaths[0], nil
	} else {
		if runtime.GOOS != "windows" {
			// search XDG data directories on non windows platform
			// symlink all assets into XDG_RUNTIME_DIR so xray-core can find them
			relpath := filepath.Join(folder, filename)
			fullpath, err := xdg.SearchDataFile(relpath)
			if err != nil {
				fullpath, err = xdg.DataFile(relpath)
				if err != nil {
					return "", err
				}
			}
			runtimepath := filepath.Join(runtimeAssetDir(), filename)
			os.Remove(runtimepath)
			err = os.Symlink(fullpath, runtimepath)
			if err != nil {
				return "", err
			}
			return fullpath, err
		} else {
			// fallback to the old behavior of using only config dir on windows
			return filepath.Join(conf.GetEnvironmentConfig().Config, filename), nil
		}
	}
}

// coreAssets are the files v2raya_core itself loads by name; it is given
// XRAY_LOCATION_ASSET and looks nowhere else.
var coreAssets = []string{"geoip.dat", "geosite.dat", "LoyalsoldierSite.dat", "geoip-only-cn-private.dat"}

// EnsureCoreAssets links the dat files the core needs into assetDir when they
// live somewhere else. v2rayA only ever created those links as a side effect
// of looking a file up for itself, and on a system whose XDG runtime directory
// is cleared between sessions the core then started with an empty asset
// directory and failed with "failed to open geosite.dat" while the files sat
// in /usr/share/v2raya all along.
func EnsureCoreAssets(assetDir string) {
	if runtime.GOOS == "windows" || assetDir == "" {
		return
	}
	for _, name := range coreAssets {
		target := filepath.Join(assetDir, name)
		if _, err := os.Stat(target); err == nil {
			continue
		}
		source := findAssetOutsideDir(name, assetDir)
		if source == "" {
			continue
		}
		if err := os.MkdirAll(assetDir, 0755); err != nil {
			log.Warn("cannot create the asset directory %v: %v", assetDir, err)
			return
		}
		_ = os.Remove(target)
		if err := os.Symlink(source, target); err != nil {
			log.Warn("cannot link %v into %v: %v", source, assetDir, err)
			continue
		}
		log.Info("linked %v into the core asset directory %v", source, assetDir)
	}
}

// findAssetOutsideDir returns the first readable copy of name that is not
// already in assetDir, searching the XDG data directories and the two system
// directories a distribution package installs into.
func findAssetOutsideDir(name string, assetDir string) string {
	var candidates []string
	if p, err := xdg.SearchDataFile(filepath.Join("v2raya", name)); err == nil {
		candidates = append(candidates, p)
	}
	candidates = append(candidates,
		filepath.Join("/usr/local/share", "v2raya", name),
		filepath.Join("/usr/share", "v2raya", name),
	)
	for _, c := range candidates {
		if filepath.Dir(c) == filepath.Clean(assetDir) {
			continue
		}
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// xdgAssetDirs returns the data directories the XDG data file search visits,
// in the order it visits them: the user's data home first, then every entry of
// XDG_DATA_DIRS.
func xdgAssetDirs() []string {
	return append([]string{xdg.DataHome}, xdg.DataDirs...)
}

// IsRuntimeAssetDir reports whether dir is the runtime directory v2rayA points
// the core at when no asset directory is configured. Only that directory is
// filled from every data directory: one named explicitly (--v2ray-assetsdir)
// is left holding exactly what its owner put there.
func IsRuntimeAssetDir(dir string) bool {
	if runtime.GOOS == "windows" || dir == "" {
		return false
	}
	return filepath.Clean(dir) == filepath.Clean(runtimeAssetDir())
}

// xdgAsset is one rule data file name found under v2raya/ in the data
// directories, together with the copy the search order gives precedence to.
type xdgAsset struct {
	name    string   // file name, linked as it is spelled
	path    string   // the copy that wins
	ignored []string // copies in directories visited later
}

// LinkXDGAssets links every dat file the data directories hold under v2raya/
// into assetDir, the one directory the core is told to search.
//
// A distribution that installs geoip.dat into /usr/share/v2raya, or an
// administrator who drops custom.dat into ~/.local/share/v2raya, then needs no
// second copy in the runtime directory, which is cleared between sessions and
// could not be written by a service user at all.
//
// A name that is in more than one data directory is resolved the way the data
// file search resolves it: the directory visited first wins, and the copies
// that lose are left alone and reported as warnings, one per name, with the
// path of each one. Nothing is done on Windows, where the configured directory
// holds the files themselves rather than links to them.
func LinkXDGAssets(assetDir string) []string {
	if runtime.GOOS == "windows" || assetDir == "" {
		return nil
	}
	assets := discoverXDGAssets()
	linkAssets(assets, assetDir)
	return duplicateWarnings(assets)
}

// duplicateWarnings describes every name found in more than one data
// directory, so that a copy the search order hides is not dropped without a
// word. Directories are visited home first, so the losing copies are the ones
// listed after the winner.
func duplicateWarnings(assets []xdgAsset) []string {
	var warnings []string
	for _, a := range assets {
		if len(a.ignored) == 0 {
			continue
		}
		warnings = append(warnings, fmt.Sprintf(
			"rule data %v is in more than one directory: using %v and ignoring %v",
			a.name, a.path, strings.Join(a.ignored, ", ")))
	}
	return warnings
}

// linkAssets links each winning copy into assetDir, replacing a link that
// points somewhere else and leaving an identical one alone. Failing to link a
// single file is logged and does not stop the others.
func linkAssets(assets []xdgAsset, assetDir string) {
	if len(assets) == 0 {
		return
	}
	if err := os.MkdirAll(assetDir, 0755); err != nil {
		log.Warn("cannot create the asset directory %v: %v", assetDir, err)
		return
	}
	for _, a := range assets {
		target := filepath.Join(assetDir, a.name)
		if link, err := os.Readlink(target); err == nil && link == a.path {
			// The link already points at the copy that wins.
			continue
		}
		_ = os.Remove(target)
		if err := os.Symlink(a.path, target); err != nil {
			log.Warn("cannot link %v into %v: %v", a.path, assetDir, err)
			continue
		}
		log.Info("linked %v into the core asset directory %v", a.path, assetDir)
	}
}

// discoverXDGAssets walks the data directories in search order and returns one
// entry per dat file name: the copy that wins and the ones it shadows.
func discoverXDGAssets() []xdgAsset {
	index := make(map[string]int)
	var assets []xdgAsset
	for _, dir := range xdgAssetDirs() {
		sub := filepath.Join(dir, "v2raya")
		for _, name := range datFilesIn(sub) {
			path := filepath.Join(sub, name)
			key := assetKey(name)
			if i, ok := index[key]; ok {
				assets[i].ignored = append(assets[i].ignored, path)
				continue
			}
			index[key] = len(assets)
			assets = append(assets, xdgAsset{name: name, path: path})
		}
	}
	return assets
}

// datFilesIn returns the regular dat files directly inside dir, sorted so that
// the result does not depend on the filesystem. A missing directory is normal:
// not every data directory has a v2raya subdirectory.
func datFilesIn(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !strings.EqualFold(filepath.Ext(e.Name()), ".dat") {
			continue
		}
		info, err := os.Stat(filepath.Join(dir, e.Name()))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// assetKey is the name two copies are compared by. On macOS the data
// directories sit on a case-insensitive filesystem, where differently cased
// names are one file and the second link would shadow the first.
func assetKey(name string) string {
	if runtime.GOOS == "darwin" {
		return strings.ToLower(name)
	}
	return name
}

func DoesV2rayAssetExist(filename string) bool {
	fullpath, err := GetV2rayLocationAsset(filename)
	if err != nil {
		return false
	}
	_, err = os.Stat(fullpath)
	if err != nil {
		return false
	}
	return true
}

func GFWListMissingError() error {
	dir := GetV2rayLocationAssetOverride()
	return common.Coded("GFWLIST_MISSING", fmt.Errorf("GFWList mode needs LoyalsoldierSite.dat, which is missing from %s; update GFWList first", dir), map[string]interface{}{"dir": dir})
}

func GetGFWListModTime() (time.Time, error) {
	fullpath, err := GetV2rayLocationAsset("LoyalsoldierSite.dat")
	if err != nil {
		return time.Now(), err
	}
	return files.GetFileModTime(fullpath)
}

// GetGeoSiteModTime returns the modification time of the standard GeoSite
// database. Unlike GetGFWListModTime, this file is not the GFWList download.
func GetGeoSiteModTime() (time.Time, error) {
	fullpath, err := GetV2rayLocationAsset("geosite.dat")
	if err != nil {
		return time.Now(), err
	}
	return files.GetFileModTime(fullpath)
}

func GetConfigBytes() (b []byte, err error) {
	b, err = os.ReadFile(GetV2rayConfigPath())
	if err != nil {
		log.Warn("failed to get config: %v", err)
		return
	}
	b = jsonc.ToJSON(b)
	return
}

func GetV2rayConfigPath() (p string) {
	return path.Join(conf.GetEnvironmentConfig().Config, "config.json")
}

func GetV2rayConfigDirPath() (p string) {
	return conf.GetEnvironmentConfig().V2rayConfigDirectory
}

func GetNftablesConfigPath() (p string) {
	return path.Join(conf.GetEnvironmentConfig().Config, "v2raya.nft")
}

func Download(url string, to string) (err error) {
	c := &http.Client{Timeout: 90 * time.Second}
	return download(c, url, to)
}

func download(c *http.Client, url string, to string) (err error) {
	log.Info("Downloading %v to %v", url, to)
	host := "unknown host"
	if u, parseErr := url2.Parse(url); parseErr == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	status := ""
	resp, err := c.Get(url)
	if err != nil || resp.StatusCode != 200 {
		if err == nil {
			defer resp.Body.Close()
			status = resp.Status
			err = fmt.Errorf("download from %s failed: HTTP %s", host, status)
		} else {
			// The request never got a reply, so there is no status to show;
			// a message that ends in "HTTP )" tells the user nothing.
			reason := err
			for {
				inner := errors.Unwrap(reason)
				if inner == nil {
					break
				}
				reason = inner
			}
			return common.Coded("ASSET_UNREACHABLE", err, map[string]interface{}{
				"host":   host,
				"detail": reason.Error(),
			})
		}
		return common.Coded("ASSET_DOWNLOAD_FAILED", err, map[string]interface{}{
			"host":   host,
			"status": status,
		})
	}
	defer resp.Body.Close()
	status = resp.Status
	b, err := readAssetBody(resp)
	if err != nil {
		return common.Coded("ASSET_DOWNLOAD_FAILED", err, map[string]interface{}{
			"host":   host,
			"status": status,
		})
	}
	if err = os.WriteFile(to, b, 0644); err != nil {
		return common.Coded("ASSET_DOWNLOAD_FAILED", err, map[string]interface{}{
			"host":   host,
			"status": status,
		})
	}
	return nil
}
