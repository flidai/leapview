// Package mapasset owns the immutable, content-addressed cartographic style
// packages available to compiled visualization specifications.
package mapasset

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

const (
	StyleSHA256              = "eeb32e219ad7dd4178377e21a2f11477b44408ab44a4878579692315add1e7f7"
	ArchiveSHA256            = "af527dbc24444b4f87e89190319f65bd6e6e6ef0db8d8054f19e2017932ab392"
	BasemapAssetsRevision    = "028c18f713baecad011301ff7a69acc39bcc2ae7"
	assetID                  = "leapview-streets"
	mapAssetURLPrefix        = "/map-assets/" + assetID
	installedAssetPathPrefix = assetID
)

var assets = map[string]visualizationir.VisualizationMapStyleAsset{
	"streets": {
		ID:            assetID,
		StyleURL:      mapAssetURLPrefix + "/styles/" + StyleSHA256 + "/style.json",
		StyleDigest:   "sha256:" + StyleSHA256,
		ArchiveURL:    mapAssetURLPrefix + "/archives/" + ArchiveSHA256 + "/basemap.pmtiles",
		ArchiveDigest: "sha256:" + ArchiveSHA256,
		GlyphsURL:     mapAssetURLPrefix + "/assets/" + BasemapAssetsRevision + "/glyphs/{fontstack}/{range}.pbf",
		SpriteURL:     mapAssetURLPrefix + "/assets/" + BasemapAssetsRevision + "/sprites/leapview",
		Source:        "OpenStreetMap contributors; global context through zoom 6 with South America regional detail through zoom 10, packaged as an immutable LeapView vector basemap",
		License:       "Open Database License 1.0 (data); BSD-3-Clause (style)",
		Attribution:   "© OpenStreetMap contributors",
		MinimumZoom:   0,
		MaximumZoom:   10,
		Bounds:        []float64{-180, -85.051129, 180, 85.051129},
		LabelAnchor:   "address_label",
	},
}

// File is one immutable file in the installed basemap package. Path is
// relative to the configured map-asset root and Digest is a raw SHA-256 hex
// digest. The complete list is compiled into the binary so readiness cannot
// be forged by rewriting a sidecar manifest beside corrupted assets.
type File struct {
	Path   string
	Digest string
}

type verifiedFile struct {
	size    int64
	mode    os.FileMode
	modTime int64
}

// Verifier continuously proves the installed immutable package matches the
// inventory compiled into the binary. Unchanged files are checked with cheap
// metadata reads; files whose size, mode, or modification time changed are
// rehashed before readiness succeeds.
type Verifier struct {
	root   string
	files  []File
	mu     sync.Mutex
	cache  map[string]verifiedFile
	hashed int
}

func NewVerifier(root string) *Verifier {
	return newVerifier(root, ExpectedFiles())
}

func newVerifier(root string, files []File) *Verifier {
	return &Verifier{root: strings.TrimSpace(root), files: append([]File(nil), files...), cache: map[string]verifiedFile{}}
}

var supportingDigests = map[string]string{
	"glyphs/Noto Sans Italic/0-255.pbf":        "43edfca91c285ba1226f09d5e74d68e1473a088c517f02a5212ff6ccb10037dc",
	"glyphs/Noto Sans Italic/1024-1279.pbf":    "9c3f67dce1f538ee635405a5562c364c95ba4db06a6b312a45c84ad6edf4c28a",
	"glyphs/Noto Sans Italic/11520-11775.pbf":  "92ca296571538164d95366e99a0e7cb15a63c8051db39765e3aba531f9ae0df8",
	"glyphs/Noto Sans Italic/1280-1535.pbf":    "12c88da54998281f49363094120d158ab9fd956573c724f632aa6745be998566",
	"glyphs/Noto Sans Italic/1536-1791.pbf":    "130f950ea1de60403501b681f786514f3a57a06b2304dd77400634a8ddfd8a83",
	"glyphs/Noto Sans Italic/256-511.pbf":      "a6f9f6574c86a4a28aba630ca1017857ceadd2370ed32a1293c35f819ab9bd60",
	"glyphs/Noto Sans Italic/3840-4095.pbf":    "6b4292518f10a3a81e61c9def6da5f26151eade86569a7721f00536f3580d70e",
	"glyphs/Noto Sans Italic/4096-4351.pbf":    "de6973edeab19903ac99866f4628a0a040cb525e71224c88db82fc2057e435c3",
	"glyphs/Noto Sans Italic/512-767.pbf":      "5849298255f12639422a8b8ef32927b1ac49d59f725afaa6cbaf51550a5752fe",
	"glyphs/Noto Sans Italic/768-1023.pbf":     "52bfd64775b91f7847da983b7bbc277135e006423cec22a44499e03e182e614e",
	"glyphs/Noto Sans Medium/0-255.pbf":        "ba2f0118dd024e3041b158e5f9eb49bc0a658019f53f458e9f5c0b8efcd79b91",
	"glyphs/Noto Sans Medium/1024-1279.pbf":    "1fd3385560bae824ee48de8c920412ca3a75cabe5ada5ea67d8d188787018319",
	"glyphs/Noto Sans Medium/11520-11775.pbf":  "1ffa4fb1f5fc1fff94939305bd15e536f739f7bc6496b6ed275e55ab71291a23",
	"glyphs/Noto Sans Medium/1280-1535.pbf":    "655ca6c720bba0bbfc5c6e916eac896a38f6c92346cead859dcbbc7a6ee7544e",
	"glyphs/Noto Sans Medium/1536-1791.pbf":    "e998806d6e6b4c03950564e64be0d91c0cfc576c886d698953a42d33f2ccc099",
	"glyphs/Noto Sans Medium/256-511.pbf":      "d5e801a1a5b1d409d3298c3a1e1ca76328e2314a751078833a618620e8e66e4d",
	"glyphs/Noto Sans Medium/3840-4095.pbf":    "721045c6ce264a9314db10955af1537dba09e0e6a3256d1c0fbd5e86b7cc9ff2",
	"glyphs/Noto Sans Medium/4096-4351.pbf":    "08b5ec865c28a4b151f5b7bd6c7bd83e6a015ef992257620f69a7f576f256f44",
	"glyphs/Noto Sans Medium/512-767.pbf":      "985e56060887a8e774fdf62ec4619f26bd44d264fcb837d315023f87d768f8e2",
	"glyphs/Noto Sans Medium/768-1023.pbf":     "4b92939b4d3779b2d0b6be5b69c105e8c88352c047170e474011e82511ac03e9",
	"glyphs/Noto Sans Regular/0-255.pbf":       "62c6d49b15fa836eb6aa45e259c7ca6762f44b011b09e47776efbe4a6db1b397",
	"glyphs/Noto Sans Regular/1024-1279.pbf":   "302231023f7048d9694a7b3f3b737c8bc378f5af0175aed5bb4f2f3b6188919a",
	"glyphs/Noto Sans Regular/11520-11775.pbf": "59d1bc7f5596128a7ffb6ce801a784d3c2dbf8c9f0736fca85a38cb634a8b6e7",
	"glyphs/Noto Sans Regular/1280-1535.pbf":   "34345ffc1a3c6748147e079a7971a4ef10561f721fdf76a4a91491280ab8bccb",
	"glyphs/Noto Sans Regular/1536-1791.pbf":   "f46850e6574d817d58d96b91196739c77c1b570a7b4f992ff1380d2b4d5d631f",
	"glyphs/Noto Sans Regular/256-511.pbf":     "2eca7561f9f566bcacfda5dd04fb5880baec1328ec0f5484678289a13994de8a",
	"glyphs/Noto Sans Regular/3840-4095.pbf":   "d5a6f6e24c0f7df4c02824831f68b8564b90ae81ed919ed79ce7c4a1338b7808",
	"glyphs/Noto Sans Regular/4096-4351.pbf":   "f08764a2e60f5d2e5b1d072668aee1be18a04fe542af38dcee419f529c13433c",
	"glyphs/Noto Sans Regular/512-767.pbf":     "6abc80badad0e823e228ab739515be257c8b33bc68bb077dd873030e9b2d143a",
	"glyphs/Noto Sans Regular/768-1023.pbf":    "537ccbee79180f4f8b3f6d2bdd27df979260ce1a0358c40d7aa7db505d9b03aa",
	"sprites/leapview.json":                    "bfac76cf7ed5c2aa2992695904056a1c6b07785b7fd20e6c640cb44fd6244a2e",
	"sprites/leapview.png":                     "b6a34640917bdc57d0bd080836db33376371a3312ebe7b849045268015de3481",
	"sprites/leapview@2x.json":                 "1fb5b123fbe35d2e1f6ac171513ce01df89e671273a417ee89c44886a4c132f0",
	"sprites/leapview@2x.png":                  "23f6e9df27c2e9a14385763980a24e3608966bfc8192a106058bc2ca959ab563",
}

var (
	packageFiles = buildExpectedFiles()
	packageURLs  = buildExpectedURLSet(packageFiles)
)

// Resolve returns a complete provenance record for a public authoring asset.
func Resolve(id string) (visualizationir.VisualizationMapStyleAsset, error) {
	asset, ok := assets[id]
	if !ok {
		return visualizationir.VisualizationMapStyleAsset{}, fmt.Errorf("unknown map style asset %q", id)
	}
	return asset, nil
}

// ExpectedFiles returns the deterministic package inventory.
func ExpectedFiles() []File {
	return append([]File(nil), packageFiles...)
}

func buildExpectedFiles() []File {
	files := []File{
		{Path: installedAssetPathPrefix + "/styles/" + StyleSHA256 + "/style.json", Digest: StyleSHA256},
		{Path: installedAssetPathPrefix + "/archives/" + ArchiveSHA256 + "/basemap.pmtiles", Digest: ArchiveSHA256},
	}
	for relative, digest := range supportingDigests {
		files = append(files, File{Path: installedAssetPathPrefix + "/assets/" + BasemapAssetsRevision + "/" + relative, Digest: digest})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

func buildExpectedURLSet(files []File) map[string]struct{} {
	paths := make(map[string]struct{}, len(files))
	for _, file := range files {
		paths["/map-assets/"+file.Path] = struct{}{}
	}
	return paths
}

// VerifyInstalled proves that every file in the configured package exists and
// matches the digest compiled into this binary.
func VerifyInstalled(root string) error {
	return NewVerifier(root).Verify(context.Background())
}

func (v *Verifier) Verify(ctx context.Context) error {
	if v == nil || v.root == "" {
		return fmt.Errorf("map asset root is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, expected := range v.files {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := filepath.Join(v.root, filepath.FromSlash(expected.Path))
		info, err := os.Stat(name)
		if os.IsNotExist(err) {
			delete(v.cache, expected.Path)
			return fmt.Errorf("map asset %s is missing", expected.Path)
		}
		if err != nil {
			delete(v.cache, expected.Path)
			return fmt.Errorf("stat map asset %s: %w", expected.Path, err)
		}
		fingerprint := verifiedFile{size: info.Size(), mode: info.Mode(), modTime: info.ModTime().UnixNano()}
		if fingerprint == v.cache[expected.Path] {
			continue
		}
		actual, err := hashInstalledFile(ctx, name)
		v.hashed++
		if err != nil {
			delete(v.cache, expected.Path)
			return fmt.Errorf("hash map asset %s: %w", expected.Path, err)
		}
		if actual != expected.Digest {
			delete(v.cache, expected.Path)
			return fmt.Errorf("map asset %s digest mismatch: got %s", expected.Path, actual)
		}
		v.cache[expected.Path] = fingerprint
	}
	return nil
}

func (v *Verifier) hashedFiles() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.hashed
}

func hashInstalledFile(ctx context.Context, name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	buffer := make([]byte, 256*1024)
	for {
		if err := ctx.Err(); err != nil {
			file.Close()
			return "", err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			if _, err := hash.Write(buffer[:count]); err != nil {
				file.Close()
				return "", err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			file.Close()
			return "", readErr
		}
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// IsContentAddressedURLPath reports whether path identifies one exact file in
// the compiled package inventory. Unknown revisions and legacy mutable paths
// fail closed before reaching the filesystem.
func IsContentAddressedURLPath(value string) bool {
	decoded, err := url.PathUnescape(value)
	if err != nil || !strings.HasPrefix(decoded, "/map-assets/") {
		return false
	}
	relative := strings.TrimPrefix(decoded, "/map-assets/")
	_, ok := packageURLs["/map-assets/"+relative]
	return ok
}
