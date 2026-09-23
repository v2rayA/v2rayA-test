package asset

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/adrg/xdg"
)

type assetTransport func(*http.Request) (*http.Response, error)

func (f assetTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAssetBodyOverLimitIsRejected(t *testing.T) {
	client := &http.Client{Transport: assetTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", int(maxAssetDownloadSize+1)))),
		}, nil
	})}
	target := filepath.Join(t.TempDir(), "asset.dat")
	if err := download(client, "https://example.test/asset.dat", target); err == nil || !strings.Contains(err.Error(), "256 MiB") {
		t.Fatalf("error = %v, want size limit", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("oversized asset created target: %v", err)
	}
}

// The core only reads XRAY_LOCATION_ASSET, so a dat file that exists in a
// system directory has to be linked into that directory before the core runs.
func TestEnsureCoreAssetsLinksMissingFiles(t *testing.T) {
	system := t.TempDir()
	assetDir := filepath.Join(t.TempDir(), "runtime")
	source := filepath.Join(system, "geosite.dat")
	if err := os.WriteFile(source, []byte("dat"), 0644); err != nil {
		t.Fatal(err)
	}
	// findAssetOutsideDir searches fixed system paths, so exercise the linking
	// itself with the source it would have found.
	if err := os.MkdirAll(assetDir, 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(assetDir, "geosite.dat")
	if err := os.Symlink(source, target); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(target)
	if err != nil || string(b) != "dat" {
		t.Fatalf("link does not resolve: %v %q", err, b)
	}
	// An asset already present must not be touched.
	EnsureCoreAssets(assetDir)
	fi, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the existing link was replaced")
	}
}

func TestFindAssetOutsideDirSkipsItsOwnDirectory(t *testing.T) {
	dir := "/usr/share/v2raya"
	if _, err := os.Stat(filepath.Join(dir, "geosite.dat")); err != nil {
		t.Skip("no system geosite.dat on this machine")
	}
	if got := findAssetOutsideDir("geosite.dat", dir); got != "" {
		t.Errorf("a file in the asset directory itself must not be reported as a source, got %q", got)
	}
}

// namesOf returns the linked names in the order they would be created.
func namesOf(t *testing.T, assets []xdgAsset) []string {
	t.Helper()
	names := make([]string, 0, len(assets))
	for _, a := range assets {
		names = append(names, a.name)
	}
	return names
}

// The core reads one directory, so every dat file the data directories hold
// has to be found, and the copy the search visits first has to be the one that
// wins. The winners carry the paths of the copies they shadow, which is what
// the warning is built from.
func TestDiscoverXDGAssetsTakesTheFirstCopyInSearchOrder(t *testing.T) {
	dataHome, dataDir, otherDir := t.TempDir(), t.TempDir(), t.TempDir()
	withXDGDirs(t, dataHome, []string{dataDir, otherDir})

	writeAsset(t, filepath.Join(dataHome, "v2raya", "geosite.dat"), "user")
	writeAsset(t, filepath.Join(dataHome, "v2raya", "custom.dat"), "custom")
	writeAsset(t, filepath.Join(dataDir, "v2raya", "geosite.dat"), "system")
	writeAsset(t, filepath.Join(dataDir, "v2raya", "geoip.dat"), "geoip")
	writeAsset(t, filepath.Join(otherDir, "v2raya", "geoip.dat"), "later")
	writeAsset(t, filepath.Join(otherDir, "v2raya", "geosite.dat"), "later")
	// Not rule data, not something the core could ask for.
	writeAsset(t, filepath.Join(dataDir, "v2raya", "notes.txt"), "notes")
	// A directory named like an asset, and a link to nowhere, are no assets.
	if err := os.MkdirAll(filepath.Join(dataDir, "v2raya", "dir.dat"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dataDir, "missing"), filepath.Join(dataDir, "v2raya", "dangling.dat")); err != nil {
		t.Fatal(err)
	}

	assets := discoverXDGAssets()

	// One entry per name, in the order the directories are visited and, inside
	// a directory, by name.
	if got, want := namesOf(t, assets), []string{"custom.dat", "geosite.dat", "geoip.dat"}; !slices.Equal(got, want) {
		t.Fatalf("assets = %v, want %v", got, want)
	}
	for i, wantPath := range []string{
		filepath.Join(dataHome, "v2raya", "custom.dat"),
		filepath.Join(dataHome, "v2raya", "geosite.dat"),
		filepath.Join(dataDir, "v2raya", "geoip.dat"),
	} {
		if assets[i].path != wantPath {
			t.Errorf("%v wins from %v, want %v", assets[i].name, assets[i].path, wantPath)
		}
	}
	if got := assets[0].ignored; len(got) != 0 {
		t.Errorf("custom.dat shadows %v, want nothing", got)
	}
	// The data home wins over the data directories, and the first data
	// directory wins over the second.
	if got, want := assets[1].ignored, []string{
		filepath.Join(dataDir, "v2raya", "geosite.dat"),
		filepath.Join(otherDir, "v2raya", "geosite.dat"),
	}; !slices.Equal(got, want) {
		t.Errorf("geosite.dat shadows %v, want %v", got, want)
	}
	if got, want := assets[2].ignored, []string{filepath.Join(otherDir, "v2raya", "geoip.dat")}; !slices.Equal(got, want) {
		t.Errorf("geoip.dat shadows %v, want %v", got, want)
	}
}

// The core is pointed at the runtime asset directory alone, so the files found
// above have to be there as links, and the core has to read the winning copy.
func TestLinkXDGAssetsMirrorsDataDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the core reads the configured directory itself on windows")
	}
	dataHome, dataDir := t.TempDir(), t.TempDir()
	assetDir := filepath.Join(t.TempDir(), "runtime")
	withXDGDirs(t, dataHome, []string{dataDir})

	writeAsset(t, filepath.Join(dataHome, "v2raya", "geosite.dat"), "user")
	writeAsset(t, filepath.Join(dataHome, "v2raya", "custom.dat"), "custom")
	writeAsset(t, filepath.Join(dataDir, "v2raya", "geosite.dat"), "system")
	writeAsset(t, filepath.Join(dataDir, "v2raya", "geoip.dat"), "geoip")
	writeAsset(t, filepath.Join(dataDir, "v2raya", "notes.txt"), "notes")

	warnings := LinkXDGAssets(assetDir)

	for name, want := range map[string]string{
		"geosite.dat": "user",
		"custom.dat":  "custom",
		"geoip.dat":   "geoip",
	} {
		link := filepath.Join(assetDir, name)
		if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%v is not a symlink: %v %v", link, fi, err)
		}
		if got := readAsset(t, link); got != want {
			t.Errorf("%v holds %q, want %q", name, got, want)
		}
	}
	if _, err := os.Lstat(filepath.Join(assetDir, "notes.txt")); !os.IsNotExist(err) {
		t.Errorf("notes.txt was linked into the asset directory: %v", err)
	}

	// The shadowed copy is reported once, with both paths, instead of being
	// dropped without a word.
	if len(warnings) != 1 {
		t.Fatalf("warnings = %q, want exactly one for the duplicated name", warnings)
	}
	for _, want := range []string{"geosite.dat", filepath.Join(dataHome, "v2raya", "geosite.dat"), filepath.Join(dataDir, "v2raya", "geosite.dat")} {
		if !strings.Contains(warnings[0], want) {
			t.Errorf("warning %q does not mention %v", warnings[0], want)
		}
	}

	// Running again must keep the same result and not report anything new.
	if again := LinkXDGAssets(assetDir); len(again) != 1 || again[0] != warnings[0] {
		t.Errorf("second run reported %q, want %q", again, warnings)
	}
	if got := readAsset(t, filepath.Join(assetDir, "geosite.dat")); got != "user" {
		t.Errorf("second run replaced the winning copy with %q", got)
	}
}

// A link that already points at the winning copy is left as it is, and one
// that points at a copy the data directories no longer hold is replaced
// instead of blocking the names that are still there.
func TestLinkAssetsKeepsUpWithTheDataDirectories(t *testing.T) {
	requireSymlinks(t)
	dataHome, dataDir := t.TempDir(), t.TempDir()
	assetDir := filepath.Join(t.TempDir(), "runtime")
	withXDGDirs(t, dataHome, []string{dataDir})
	writeAsset(t, filepath.Join(dataHome, "v2raya", "geosite.dat"), "user")
	writeAsset(t, filepath.Join(dataDir, "v2raya", "geosite.dat"), "system")
	writeAsset(t, filepath.Join(dataDir, "v2raya", "geoip.dat"), "geoip")
	if err := os.MkdirAll(assetDir, 0755); err != nil {
		t.Fatal(err)
	}
	// A real file where a link belongs, and a link that dangles.
	writeAsset(t, filepath.Join(assetDir, "geosite.dat"), "copied by hand")
	if err := os.Symlink(filepath.Join(dataHome, "v2raya", "gone.dat"), filepath.Join(assetDir, "gone.dat")); err != nil {
		t.Fatal(err)
	}

	linkAssets(discoverXDGAssets(), assetDir)

	for name, want := range map[string]string{"geosite.dat": "user", "geoip.dat": "geoip"} {
		link := filepath.Join(assetDir, name)
		if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%v is not a symlink: %v %v", link, fi, err)
		}
		if got := readAsset(t, link); got != want {
			t.Errorf("%v holds %q, want %q", name, got, want)
		}
	}
	// The link that dangles is not a name the data directories hold, so
	// nothing is linked for it and nothing else is removed.
	if _, err := os.Lstat(filepath.Join(assetDir, "gone.dat")); err != nil {
		t.Errorf("an unrelated link was removed: %v", err)
	}

	// The second run has nothing left to change.
	before, err := os.Readlink(filepath.Join(assetDir, "geosite.dat"))
	if err != nil {
		t.Fatal(err)
	}
	linkAssets(discoverXDGAssets(), assetDir)
	if after, err := os.Readlink(filepath.Join(assetDir, "geosite.dat")); err != nil || after != before {
		t.Errorf("second run relinked geosite.dat: %q %v", after, err)
	}
	if got := readAsset(t, filepath.Join(assetDir, "geosite.dat")); got != "user" {
		t.Errorf("second run replaced the winning copy with %q", got)
	}
}

func TestIsRuntimeAssetDir(t *testing.T) {
	old := xdg.RuntimeDir
	t.Cleanup(func() { xdg.RuntimeDir = old })
	xdg.RuntimeDir = t.TempDir()
	runtimeDir := filepath.Join(xdg.RuntimeDir, "v2raya")

	if runtime.GOOS == "windows" {
		// Windows keeps the files in the configured directory, so nothing is
		// ever mirrored into a runtime one.
		if IsRuntimeAssetDir(runtimeDir) {
			t.Errorf("IsRuntimeAssetDir(%q) = true on windows", runtimeDir)
		}
		return
	}
	if !IsRuntimeAssetDir(runtimeDir) {
		t.Errorf("IsRuntimeAssetDir(%q) = false", runtimeDir)
	}
	if !IsRuntimeAssetDir(runtimeDir + string(filepath.Separator) + ".") {
		t.Error("the same directory spelled differently must be recognized")
	}
	for _, dir := range []string{"", t.TempDir(), filepath.Join(t.TempDir(), "v2raya")} {
		if IsRuntimeAssetDir(dir) {
			t.Errorf("IsRuntimeAssetDir(%q) = true, want false", dir)
		}
	}
}

func writeAsset(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// requireSymlinks skips a test where the process may not create a symlink,
// which on windows means the developer mode is off.
func requireSymlinks(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "target"), filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
}

func readAsset(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// withXDGDirs points the package's XDG globals at test directories, the way
// the environment would point them at real ones.
func withXDGDirs(t *testing.T, dataHome string, dataDirs []string) {
	t.Helper()
	oldHome, oldDirs := xdg.DataHome, xdg.DataDirs
	t.Cleanup(func() { xdg.DataHome, xdg.DataDirs = oldHome, oldDirs })
	xdg.DataHome, xdg.DataDirs = dataHome, dataDirs
}
