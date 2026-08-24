package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	content "github.com/flidai/leapview/docs"
	"github.com/flidai/leapview/pkg/pagestream"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

type desktopReleaseManifest struct {
	SchemaVersion int                      `json:"schemaVersion"`
	Status        string                   `json:"status"`
	Product       desktopReleaseProduct    `json:"product"`
	Channel       desktopReleaseChannel    `json:"channel"`
	Support       []desktopReleasePlatform `json:"support"`
	Release       *desktopPublishedRelease `json:"release"`
}

type desktopReleaseProduct struct {
	Name          string `json:"name"`
	ApplicationID string `json:"applicationId"`
}

type desktopReleaseChannel struct {
	Name         string `json:"name"`
	UpdateOrigin string `json:"updateOrigin"`
	PathVersion  string `json:"pathVersion"`
}

type desktopReleasePlatform struct {
	Platform       string   `json:"platform"`
	Architectures  []string `json:"architectures"`
	MinimumVersion string   `json:"minimumVersion"`
}

type desktopPublishedRelease struct {
	Version       string                   `json:"version"`
	Tag           string                   `json:"tag"`
	PublishedAt   string                   `json:"publishedAt"`
	NotesURL      string                   `json:"notesUrl"`
	EvidenceURL   string                   `json:"evidenceUrl"`
	SourceCommit  string                   `json:"sourceCommit"`
	SigningStatus string                   `json:"signingStatus"`
	Artifacts     []desktopReleaseArtifact `json:"artifacts"`
}

type desktopReleaseArtifact struct {
	Platform      string                  `json:"platform"`
	Architecture  string                  `json:"architecture"`
	Format        string                  `json:"format"`
	FileName      string                  `json:"fileName"`
	Bytes         int64                   `json:"bytes"`
	DownloadURL   string                  `json:"downloadUrl"`
	SHA256        string                  `json:"sha256"`
	ChecksumURL   string                  `json:"checksumUrl"`
	ProvenanceURL string                  `json:"provenanceUrl"`
	SBOMURL       string                  `json:"sbomUrl"`
	Signature     desktopReleaseSignature `json:"signature"`
}

type desktopReleaseSignature struct {
	Type     string `json:"type"`
	Identity string `json:"identity"`
}

var desktopRelease = loadDesktopReleaseManifest()

var (
	desktopVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)
	desktopCommitPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	desktopSHA256Pattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func loadDesktopReleaseManifest() desktopReleaseManifest {
	contents, err := content.Files.ReadFile("desktop-release.json")
	if err != nil {
		panic(fmt.Sprintf("read desktop release manifest: %v", err))
	}
	var manifest desktopReleaseManifest
	if err := json.Unmarshal(contents, &manifest); err != nil {
		panic(fmt.Sprintf("decode desktop release manifest: %v", err))
	}
	if err := validateDesktopReleaseManifest(manifest); err != nil {
		panic(fmt.Sprintf("validate desktop release manifest: %v", err))
	}
	return manifest
}

func validateDesktopReleaseManifest(manifest desktopReleaseManifest) error {
	if manifest.SchemaVersion != 1 {
		return fmt.Errorf("schema version must be 1")
	}
	if manifest.Product.Name != siteBrandName || manifest.Product.ApplicationID != "dev.leapview.desktop" {
		return fmt.Errorf("product identity is invalid")
	}
	if manifest.Channel.PathVersion != "v1" ||
		(manifest.Channel.Name == "stable" &&
			manifest.Channel.UpdateOrigin != "https://releases.leapview.dev") ||
		(manifest.Channel.Name == "preview" && manifest.Channel.UpdateOrigin != "") ||
		(manifest.Channel.Name != "stable" && manifest.Channel.Name != "preview") {
		return fmt.Errorf("release channel identity is invalid")
	}
	if len(manifest.Support) != 3 {
		return fmt.Errorf("support matrix must contain macOS, Linux, and Windows")
	}
	seen := make(map[string]struct{}, len(manifest.Support))
	for _, support := range manifest.Support {
		if _, duplicate := seen[support.Platform]; duplicate {
			return fmt.Errorf("support platform %q is duplicated", support.Platform)
		}
		seen[support.Platform] = struct{}{}
		if support.MinimumVersion == "" || len(support.Architectures) == 0 {
			return fmt.Errorf("support platform %q is incomplete", support.Platform)
		}
	}
	if manifest.Status == "preparing" {
		if manifest.Release != nil {
			return fmt.Errorf("preparing channel cannot expose a release")
		}
		return nil
	}
	if manifest.Status != "published" && manifest.Status != "withdrawn" {
		return fmt.Errorf("status %q is unsupported", manifest.Status)
	}
	if manifest.Release == nil {
		return fmt.Errorf("%s channel requires release metadata", manifest.Status)
	}
	return validateDesktopPublishedRelease(manifest)
}

func validateDesktopPublishedRelease(manifest desktopReleaseManifest) error {
	release := manifest.Release
	if !desktopVersionPattern.MatchString(release.Version) {
		return fmt.Errorf("release source identity is invalid")
	}
	if manifest.Channel.Name == "preview" {
		if release.Tag != "desktop-v"+release.Version ||
			release.SigningStatus != "unsigned" ||
			!trustedDesktopPreviewURL(release.NotesURL, release.Tag, false) ||
			release.EvidenceURL != release.NotesURL ||
			release.PublishedAt != "" || release.SourceCommit != "" {
			return fmt.Errorf("preview release identity is invalid")
		}
	} else {
		if release.Tag != "" || release.SigningStatus != "signed" ||
			!desktopCommitPattern.MatchString(release.SourceCommit) {
			return fmt.Errorf("stable release identity is invalid")
		}
		publishedAt, err := time.Parse(time.RFC3339, release.PublishedAt)
		if err != nil || publishedAt.Format(time.RFC3339) != release.PublishedAt {
			return fmt.Errorf("release publication time is invalid")
		}
		if !trustedDesktopNotesURL(release.NotesURL, release.Version) {
			return fmt.Errorf("release notes URL is invalid")
		}
	}

	expectedTargets := make(map[string]struct{})
	for _, support := range manifest.Support {
		for _, architecture := range support.Architectures {
			expectedTargets[support.Platform+"/"+architecture] = struct{}{}
		}
	}
	if len(release.Artifacts) != len(expectedTargets) {
		return fmt.Errorf("release must contain every supported target")
	}

	seen := make(map[string]struct{}, len(release.Artifacts))
	for _, artifact := range release.Artifacts {
		target := artifact.Platform + "/" + artifact.Architecture
		if _, supported := expectedTargets[target]; !supported {
			return fmt.Errorf("release target %q is unsupported", target)
		}
		if _, duplicate := seen[target]; duplicate {
			return fmt.Errorf("release target %q is duplicated", target)
		}
		seen[target] = struct{}{}
		if err := validateDesktopReleaseArtifact(
			artifact,
			release.Version,
			release.Tag,
			manifest.Channel.Name,
		); err != nil {
			return fmt.Errorf("%s: %w", target, err)
		}
	}
	return nil
}

func validateDesktopReleaseArtifact(
	artifact desktopReleaseArtifact,
	version, tag, channel string,
) error {
	expectedFormat := map[string]string{"darwin": "dmg", "linux": "deb", "win32": "exe"}[artifact.Platform]
	expectedSignature := map[string]string{
		"darwin": "developer-id-application",
		"linux":  "apt-repository",
		"win32":  "authenticode",
	}[artifact.Platform]
	if artifact.Format != expectedFormat ||
		path.Ext(artifact.FileName) != "."+expectedFormat ||
		path.Base(artifact.FileName) != artifact.FileName ||
		strings.Contains(artifact.FileName, `\`) {
		return fmt.Errorf("artifact file identity is invalid")
	}
	if channel == "preview" {
		expectedTarget := map[string]string{
			"darwin/arm64": "macos-arm64",
			"darwin/x64":   "macos-x64",
			"linux/x64":    "linux-x64",
			"win32/x64":    "windows-x64",
		}[artifact.Platform+"/"+artifact.Architecture]
		expectedFile := "LeapView-Desktop-" + version + "-" + expectedTarget + "." + expectedFormat
		if artifact.FileName != expectedFile ||
			artifact.Signature.Type != "unsigned" ||
			artifact.Signature.Identity != "Unsigned early preview" ||
			artifact.Bytes != 0 || artifact.SHA256 != "" ||
			artifact.ChecksumURL != "" || artifact.ProvenanceURL != "" ||
			artifact.SBOMURL != "" ||
			!trustedDesktopPreviewURL(artifact.DownloadURL, tag, true) {
			return fmt.Errorf("preview artifact identity is invalid")
		}
		download, _ := url.Parse(artifact.DownloadURL)
		if path.Base(download.Path) != artifact.FileName {
			return fmt.Errorf("preview artifact URL does not match its file name")
		}
		return nil
	}
	if artifact.Bytes <= 0 || !desktopSHA256Pattern.MatchString(artifact.SHA256) {
		return fmt.Errorf("artifact digest identity is invalid")
	}
	if artifact.Signature.Type != expectedSignature || strings.TrimSpace(artifact.Signature.Identity) == "" {
		return fmt.Errorf("artifact signature identity is invalid")
	}

	releasePrefix := "/desktop/v1/releases/" + version + "/" + artifact.Platform + "/" + artifact.Architecture + "/"
	if !trustedDesktopReleaseURL(artifact.DownloadURL, releasePrefix) {
		return fmt.Errorf("artifact download URL is invalid")
	}
	download, _ := url.Parse(artifact.DownloadURL)
	if path.Base(download.Path) != artifact.FileName {
		return fmt.Errorf("artifact download URL does not match its file name")
	}
	for label, rawURL := range map[string]string{
		"checksum":   artifact.ChecksumURL,
		"provenance": artifact.ProvenanceURL,
		"SBOM":       artifact.SBOMURL,
	} {
		if !trustedDesktopReleaseURL(rawURL, releasePrefix) {
			return fmt.Errorf("%s URL is invalid", label)
		}
	}
	return nil
}

func trustedDesktopPreviewURL(rawURL, tag string, download bool) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	prefix := "/flidai/leapview/releases/tag/" + tag
	if download {
		prefix = "/flidai/leapview/releases/download/" + tag + "/"
		return strings.HasPrefix(parsed.Path, prefix) && path.Clean(parsed.Path) == parsed.Path
	}
	return parsed.Path == prefix
}

func trustedDesktopReleaseURL(rawURL, pathPrefix string) bool {
	parsed, err := url.Parse(rawURL)
	return err == nil &&
		parsed.Scheme == "https" &&
		parsed.Host == "releases.leapview.dev" &&
		parsed.User == nil &&
		parsed.RawQuery == "" &&
		parsed.Fragment == "" &&
		strings.HasPrefix(parsed.Path, pathPrefix) &&
		path.Clean(parsed.Path) == parsed.Path
}

func trustedDesktopNotesURL(rawURL, version string) bool {
	parsed, err := url.Parse(rawURL)
	return err == nil &&
		parsed.Scheme == "https" &&
		parsed.Host == "leapview.dev" &&
		parsed.User == nil &&
		parsed.RawQuery == "" &&
		parsed.Fragment == "" &&
		parsed.Path == "/releases/desktop/"+version
}

func desktopDownloadPage(metadata sitePageMetadata, manifest desktopReleaseManifest) g.Node {
	return siteDocumentPage(metadata,
		h.Div(h.ID("main-content"), h.Class("site-download"),
			h.Section(h.Class("site-download-hero"),
				h.H1(g.Text("LeapView on your desktop.")),
				h.P(h.Class("site-lede"), g.Text("Connect to deployed LeapView instances from a dedicated, hardened client. Your server remains the authority for identity, access, and dashboard data.")),
				h.Div(h.Class("site-actions"),
					h.A(h.Class("site-button site-button-primary"), h.Href("/docs/desktop/install"), g.Text("Read the install guide")),
					h.A(h.Class("site-button"), h.Href("/docs/desktop/security"), g.Text("Review desktop security")),
				),
			),
			desktopReleaseStatus(manifest),
			h.Section(h.Class("site-download-section"), g.Attr("aria-labelledby", "desktop-platforms-title"),
				h.Div(h.Class("site-download-section-heading"),
					h.P(h.Class("site-eyebrow"), g.Text("Consumer v1")),
					h.H2(h.ID("desktop-platforms-title"), g.Text("Supported platforms")),
					h.P(g.Text("The first release uses familiar native formats and the operating system's trust and proxy configuration.")),
				),
				h.Div(h.Class("site-download-platforms"), g.Group(desktopPlatformCards(manifest))),
			),
			h.Section(h.Class("site-download-section site-download-steps"), g.Attr("aria-labelledby", "desktop-steps-title"),
				h.Div(h.Class("site-download-section-heading"),
					h.P(h.Class("site-eyebrow"), g.Text("How it works")),
					h.H2(h.ID("desktop-steps-title"), g.Text("Install once. Connect where you work.")),
				),
				h.Ol(
					desktopInstallStep(manifest),
					desktopDownloadStep("02", "Enter your LeapView URL", "Add the canonical HTTPS address supplied by your organization, such as analytics.company.com."),
					desktopDownloadStep("03", "Sign in with your browser", "Authentication stays with the deployed instance and your identity provider. LeapView Desktop stores no bearer token in its profile file."),
				),
			),
		),
	)
}

func desktopInstallStep(manifest desktopReleaseManifest) g.Node {
	if manifest.Channel.Name == "preview" {
		return desktopDownloadStep(
			"01",
			"Install the early preview",
			"Download the installer for your operating system from the verified GitHub prerelease. Publisher warnings are expected until production signing is available.",
		)
	}
	return desktopDownloadStep(
		"01",
		"Install the signed application",
		"Use the official installer for your operating system. End users never need source code or development tools.",
	)
}

func siteDocumentPage(metadata sitePageMetadata, body g.Node) g.Node {
	return pagestream.RenderPage(pagestream.PageSpec{
		Title:             metadata.title,
		HTMLAttrs:         siteHTMLAttrs(),
		Head:              siteHead(metadata),
		MainAttrs:         []g.Node{h.Class("site-page")},
		DatastarScriptURL: siteDatastarScriptURL,
		UpdatesURL:        "/updates",
		Body: []g.Node{
			h.A(h.Class("skip-link"), h.Href("#main-content"), g.Text("Skip to content")),
			siteHeader(false, metadata.showcase),
			body,
			siteFooter(),
		},
	})
}

func desktopReleaseStatus(manifest desktopReleaseManifest) g.Node {
	if manifest.Status == "preparing" {
		return h.Aside(h.Class("site-download-status"), g.Attr("role", "status"),
			h.Div(
				h.P(h.Class("site-eyebrow"), g.Text("Release status")),
				h.H2(g.Text("Production downloads are not published yet.")),
				h.P(g.Text("The application is being qualified across supported systems. This page will expose only signed, verified production artifacts—never pull-request or unsigned candidates.")),
			),
			h.A(h.Class("site-button"), h.Href("/docs/desktop/release-verification"), g.Text("How releases are verified")),
		)
	}
	if manifest.Status == "withdrawn" {
		return h.Aside(h.Class("site-download-status site-download-status-warning"), g.Attr("role", "alert"),
			h.Div(
				h.P(h.Class("site-eyebrow"), g.Text("Release status")),
				h.H2(g.Text("Desktop downloads are temporarily withdrawn.")),
				h.P(g.Text("No installer is currently offered. Existing installations never downgrade automatically; follow the support guide for current advice.")),
			),
			h.A(h.Class("site-button"), h.Href("/docs/desktop/support"), g.Text("Open support guidance")),
		)
	}
	if manifest.Channel.Name == "preview" {
		return h.Aside(h.Class("site-download-status site-download-status-warning"), g.Attr("role", "status"),
			h.Div(
				h.P(h.Class("site-eyebrow"), g.Text("Early preview")),
				h.H2(g.Text("LeapView Desktop "+manifest.Release.Version)),
				h.P(g.Text("These installers are not code-signed. macOS and Windows may show a publisher warning; verify the GitHub release evidence before installing.")),
			),
			h.A(h.Class("site-button"), h.Href(manifest.Release.EvidenceURL), g.Text("Verify release evidence")),
		)
	}
	return h.Aside(h.Class("site-download-status"), g.Attr("role", "status"),
		h.Div(
			h.P(h.Class("site-eyebrow"), g.Text("Stable release")),
			h.H2(g.Text("LeapView Desktop "+manifest.Release.Version)),
			h.P(g.Text("Choose the artifact matching your operating system and architecture below.")),
		),
		h.A(h.Class("site-button"), h.Href(manifest.Release.NotesURL), g.Text("Read release notes")),
	)
}

func desktopPlatformCards(manifest desktopReleaseManifest) []g.Node {
	cards := make([]g.Node, 0, len(manifest.Support))
	for _, platform := range manifest.Support {
		name, architecture, format := desktopPlatformCopy(platform)
		cards = append(cards, h.Article(h.Class("site-download-platform"),
			h.Div(h.Class("site-download-platform-heading"),
				h.H3(g.Text(name)),
				h.Span(g.Text(format)),
			),
			h.Dl(
				h.Div(h.Dt(g.Text("Version")), h.Dd(g.Text(platform.MinimumVersion+" or newer"))),
				h.Div(h.Dt(g.Text("Architecture")), h.Dd(g.Text(architecture))),
				h.Div(h.Dt(g.Text("Updates")), h.Dd(g.Text(desktopUpdateCopy(manifest.Channel.Name, platform.Platform)))),
			),
			desktopPlatformActions(manifest, platform),
		))
	}
	return cards
}

func desktopPlatformCopy(platform desktopReleasePlatform) (name, architecture, format string) {
	switch platform.Platform {
	case "darwin":
		return "macOS", "Intel and Apple silicon", "DMG"
	case "linux":
		return "Ubuntu", "x64", "DEB · APT"
	case "win32":
		return "Windows", "x64", "Squirrel EXE"
	default:
		return platform.Platform, "Unsupported", "Unavailable"
	}
}

func desktopUpdateCopy(channel, platform string) string {
	if channel == "preview" {
		return "Manual install of the next release"
	}
	if platform == "linux" {
		return "Signed APT repository"
	}
	return "Built-in stable channel"
}

func desktopPlatformActions(manifest desktopReleaseManifest, platform desktopReleasePlatform) g.Node {
	if manifest.Status != "published" {
		return h.P(h.Class("site-download-unavailable"), g.Text("Available after production signing and qualification."))
	}
	return desktopPlatformArtifacts(manifest, platform)
}

func desktopPlatformArtifacts(manifest desktopReleaseManifest, platform desktopReleasePlatform) g.Node {
	var artifacts []g.Node
	for _, artifact := range manifest.Release.Artifacts {
		if artifact.Platform != platform.Platform {
			continue
		}
		if manifest.Channel.Name == "preview" {
			artifacts = append(artifacts, h.Section(
				h.Class("site-download-artifact"),
				g.Attr("aria-label", desktopArchitectureName(artifact.Platform, artifact.Architecture)+" download"),
				h.H4(g.Text(desktopArchitectureName(artifact.Platform, artifact.Architecture))),
				h.A(
					h.Class("site-button site-button-primary"),
					h.Href(artifact.DownloadURL),
					g.Attr("rel", "noreferrer"),
					g.Text("Download "+strings.ToUpper(artifact.Format)),
				),
				h.P(g.Text("Unsigned early preview · verify the release evidence before installing.")),
			))
			continue
		}
		artifacts = append(artifacts, h.Section(
			h.Class("site-download-artifact"),
			g.Attr("aria-label", desktopArchitectureName(artifact.Platform, artifact.Architecture)+" download"),
			h.H4(g.Text(desktopArchitectureName(artifact.Platform, artifact.Architecture))),
			h.A(
				h.Class("site-button site-button-primary"),
				h.Href(artifact.DownloadURL),
				g.Attr("download", ""),
				g.Text("Download "+strings.ToUpper(artifact.Format)),
			),
			h.Dl(
				h.Div(h.Dt(g.Text("File")), h.Dd(g.Text(artifact.FileName))),
				h.Div(h.Dt(g.Text("SHA-256")), h.Dd(h.Code(g.Text(artifact.SHA256)))),
				h.Div(h.Dt(g.Text("Signed by")), h.Dd(g.Text(artifact.Signature.Identity))),
			),
			h.Nav(g.Attr("aria-label", "Verification files"),
				h.A(h.Href(artifact.ChecksumURL), g.Attr("rel", "noreferrer"), g.Text("Checksums")),
				h.A(h.Href(artifact.SBOMURL), g.Attr("rel", "noreferrer"), g.Text("SBOM")),
				h.A(h.Href(artifact.ProvenanceURL), g.Attr("rel", "noreferrer"), g.Text("Provenance")),
			),
		))
	}
	return h.Div(h.Class("site-download-artifacts"), g.Group(artifacts))
}

func desktopArchitectureName(platform, architecture string) string {
	if platform == "darwin" {
		if architecture == "arm64" {
			return "Apple silicon"
		}
		return "Intel"
	}
	return strings.ToUpper(architecture)
}

func docsDesktopRelease(w http.ResponseWriter, _ *http.Request) {
	contents, err := content.Files.ReadFile("desktop-release.json")
	if err != nil {
		http.Error(w, "read desktop release manifest", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(contents)
}

func desktopDownloadStep(number, title, body string) g.Node {
	return h.Li(
		h.Span(g.Attr("aria-hidden", "true"), g.Text(number)),
		h.Div(h.H3(g.Text(title)), h.P(g.Text(body))),
	)
}
