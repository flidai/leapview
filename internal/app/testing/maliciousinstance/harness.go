package maliciousinstance

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

const (
	defaultOversizedResponseBytes = 2 << 20
	maxOversizedResponseBytes     = 16 << 20
	maxReportBytes                = 32 << 10
	maxStoredReports              = 64
)

type Config struct {
	ExternalOrigin         string
	OversizedResponseBytes int
}

type Observation struct {
	AttackID string  `json:"attackId"`
	Outcome  Outcome `json:"outcome"`
}

type Report struct {
	RunID        string        `json:"runId"`
	Observations []Observation `json:"observations"`
}

type Harness struct {
	externalOrigin         string
	oversizedResponseBytes int
	manifest               Manifest
	attackIDs              map[string]struct{}

	mu      sync.RWMutex
	reports []Report
}

var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func New(config Config) (*Harness, error) {
	externalOrigin, err := canonicalHTTPOrigin(config.ExternalOrigin)
	if err != nil {
		return nil, fmt.Errorf("external origin: %w", err)
	}
	oversizedResponseBytes := config.OversizedResponseBytes
	if oversizedResponseBytes == 0 {
		oversizedResponseBytes = defaultOversizedResponseBytes
	}
	if oversizedResponseBytes < 1 || oversizedResponseBytes > maxOversizedResponseBytes {
		return nil, fmt.Errorf("oversized response bytes must be between 1 and %d", maxOversizedResponseBytes)
	}

	manifest := DefaultManifest()
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	attackIDs := make(map[string]struct{}, len(manifest.Attacks))
	for _, attack := range manifest.Attacks {
		attackIDs[attack.ID] = struct{}{}
	}

	return &Harness{
		externalOrigin:         externalOrigin,
		oversizedResponseBytes: oversizedResponseBytes,
		manifest:               manifest,
		attackIDs:              attackIDs,
	}, nil
}

func (h *Harness) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", h.serveIndex)
	mux.HandleFunc("GET /.well-known/leapview", h.serveDiscovery)
	mux.HandleFunc("GET /__harness/manifest.json", h.serveManifest)
	mux.HandleFunc("GET /__harness/probe.js", h.serveProbe)
	mux.HandleFunc("GET /__harness/service-worker.js", h.serveServiceWorker)
	mux.HandleFunc("POST /__harness/report", h.collectReport)
	mux.HandleFunc("GET /attack/{id}", h.serveAttack)
	return h.securityHeaders(mux)
}

func (h *Harness) Manifest() Manifest {
	manifest := h.manifest
	manifest.Attacks = append([]Attack(nil), h.manifest.Attacks...)
	return manifest
}

func (h *Harness) Reports() []Report {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return cloneReports(h.reports)
}

func (h *Harness) Reset() {
	h.mu.Lock()
	h.reports = nil
	h.mu.Unlock()
}

func (h *Harness) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (h *Harness) serveIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	setHostileContentSecurityPolicy(w)
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="en" data-harness-version="%s">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>LeapView Desktop malicious instance harness</title>
  <script src="/__harness/probe.js" defer></script>
</head>
<body>
  <main>
    <h1>Malicious instance harness</h1>
    <p>This page is intentionally hostile and must have browser-equivalent authority only.</p>
    <ol id="attacks"></ol>
  </main>
</body>
</html>`, html.EscapeString(ManifestVersion))
}

func (h *Harness) serveDiscovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"canonicalOrigin":       "https://malicious-instance.invalid",
		"instanceId":            "malicious-instance-fixture",
		"displayName":           "Malicious LeapView Instance",
		"serverVersion":         "0.0.0-harness",
		"desktopProtocolMin":    1,
		"desktopProtocolMax":    1,
		"authenticationMethods": []string{"local", "oidc"},
	})
}

func (h *Harness) serveManifest(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.manifest)
}

func (h *Harness) serveProbe(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	_, _ = io.WriteString(w, probeScript)
}

func (h *Harness) serveServiceWorker(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	_, _ = io.WriteString(w, `self.addEventListener("fetch", () => undefined);`)
}

func (h *Harness) collectReport(w http.ResponseWriter, r *http.Request) {
	if mediaType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])); mediaType != "application/json" {
		http.Error(w, "application/json is required", http.StatusUnsupportedMediaType)
		return
	}

	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxReportBytes))
	decoder.DisallowUnknownFields()
	var report Report
	if err := decoder.Decode(&report); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "report is too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid report", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "report is too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid report", http.StatusBadRequest)
		return
	}
	if err := h.validateReport(report); err != nil {
		http.Error(w, "invalid report", http.StatusBadRequest)
		return
	}

	h.mu.Lock()
	if len(h.reports) >= maxStoredReports {
		h.mu.Unlock()
		http.Error(w, "report capacity reached", http.StatusTooManyRequests)
		return
	}
	h.reports = append(h.reports, cloneReport(report))
	h.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (h *Harness) validateReport(report Report) error {
	if !runIDPattern.MatchString(report.RunID) {
		return fmt.Errorf("invalid run ID")
	}
	if len(report.Observations) > len(h.attackIDs) {
		return fmt.Errorf("too many observations")
	}
	seen := make(map[string]struct{}, len(report.Observations))
	for _, observation := range report.Observations {
		if _, ok := h.attackIDs[observation.AttackID]; !ok {
			return fmt.Errorf("unknown attack ID")
		}
		if !validOutcome(observation.Outcome) {
			return fmt.Errorf("invalid outcome")
		}
		if _, duplicate := seen[observation.AttackID]; duplicate {
			return fmt.Errorf("duplicate observation")
		}
		seen[observation.AttackID] = struct{}{}
	}
	return nil
}

func (h *Harness) serveAttack(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := h.attackIDs[id]; !ok {
		http.NotFound(w, r)
		return
	}

	switch id {
	case "navigation.cross-origin":
		http.Redirect(w, r, h.externalOrigin+"/desktop-harness-target", http.StatusTemporaryRedirect)
	case "discovery.malformed":
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"canonicalOrigin":`)
	case "discovery.oversized":
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, strings.Repeat("x", h.oversizedResponseBytes))
	case "download.hostile-filename":
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="../../LeapView Security Test.exe"`)
		_, _ = io.WriteString(w, "malicious-instance-download-fixture")
	default:
		h.serveAttackPage(w, id)
	}
}

func (h *Harness) serveAttackPage(w http.ResponseWriter, id string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	setHostileContentSecurityPolicy(w)
	externalJSON, _ := json.Marshal(h.externalOrigin + "/desktop-harness-target")
	frame := ""
	if id == "frame.cross-origin" {
		frame = fmt.Sprintf(`<iframe id="cross-origin-frame" src="%s/desktop-harness-frame"></iframe>`, html.EscapeString(h.externalOrigin))
	}
	nativeProbe := ""
	if strings.HasPrefix(id, "native.") {
		nativeProbe = `<script src="/__harness/probe.js" defer></script>`
	}
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>%s</title>%s</head>
<body data-attack-id="%s">
  <h1>%s</h1>
  <button id="trigger" type="button">Trigger attack</button>
  %s
  <script>
    const attackId = %s;
    const externalTarget = %s;
    const trigger = document.getElementById("trigger");
    if (attackId === "storage.cross-profile") {
      localStorage.setItem("leapview.desktop.harness.cross-profile", "present");
    }
    trigger.addEventListener("click", async () => {
      document.body.dataset.triggered = attackId;
      sessionStorage.setItem("leapview.desktop.harness.triggered", attackId);
      switch (attackId) {
        case "navigation.javascript": window.location.href = "javascript:document.body.dataset.javascriptExecuted='true'"; break;
        case "navigation.data": window.location.href = "data:text/html,<title>Harness data URL</title>"; break;
        case "navigation.blob": {
          const target = URL.createObjectURL(new Blob(
            ["<title>Harness blob URL</title>"],
            {type: "text/html"},
          ));
          window.location.href = target;
          break;
        }
        case "navigation.file": window.location.href = "file:///etc/passwd"; break;
        case "popup.cross-origin": window.open(externalTarget, "_blank"); break;
        case "scheme.custom": window.location.href = "leapview-harness://untrusted?command=run"; break;
        case "scheme.deep-link-injection":
          window.location.href = "leapview-desktop://open?origin=" +
            encodeURIComponent(new URL(externalTarget).origin) +
            "&path=" + encodeURIComponent("/dashboards/sales");
          break;
        case "permission.camera": await navigator.mediaDevices.getUserMedia({video:true}); break;
        case "permission.microphone": await navigator.mediaDevices.getUserMedia({audio:true}); break;
        case "permission.geolocation": navigator.geolocation.getCurrentPosition(() => {}, () => {}); break;
        case "permission.notifications": await Notification.requestPermission(); break;
        case "permission.clipboard-read": await navigator.clipboard.readText(); break;
        case "renderer.resource-exhaustion": {
          const until = performance.now() + 500;
          while (performance.now() < until) {}
          break;
        }
      }
    });
  </script>
</body>
</html>`,
		html.EscapeString(id),
		nativeProbe,
		html.EscapeString(id),
		html.EscapeString(id),
		frame,
		mustJSON(id),
		externalJSON,
	)
}

func setHostileContentSecurityPolicy(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src * data: blob: 'unsafe-inline' 'unsafe-eval'; connect-src *; frame-src * data: blob:; img-src * data: blob:")
}

func canonicalHTTPOrigin(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return "", fmt.Errorf("must be an absolute HTTP(S) origin without credentials")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("must use HTTP or HTTPS")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("must not include a path, query, or fragment")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

func cloneReports(reports []Report) []Report {
	cloned := make([]Report, len(reports))
	for index, report := range reports {
		cloned[index] = cloneReport(report)
	}
	return cloned
}

func cloneReport(report Report) Report {
	report.Observations = append([]Observation(nil), report.Observations...)
	return report
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func mustJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

const probeScript = `(() => {
  "use strict";

  const observations = [];
  const record = (attackId, outcome) => observations.push({
    attackId,
    outcome,
  });

  const rendererAuthority = Boolean(
    window.electron ||
    window.electronAPI ||
    (window.process && window.process.versions && window.process.versions.electron)
  );
  record(
    "native.renderer-authority",
    rendererAuthority ? "exposed" : "isolated",
  );

  const publish = async () => {
    await fetch("/__harness/report", {
      method: "POST",
      credentials: "omit",
      headers: {"Content-Type": "application/json"},
      body: JSON.stringify({runId: "browser-auto", observations}),
    });
  };

  const renderManifest = async () => {
    const response = await fetch("/__harness/manifest.json", {credentials: "omit"});
    const manifest = await response.json();
    const list = document.getElementById("attacks");
    for (const attack of manifest.attacks) {
      const item = document.createElement("li");
      const link = document.createElement("a");
      link.href = attack.path;
      link.textContent = attack.id + " — " + attack.title;
      item.append(link);
      list.append(item);
    }
  };

  void Promise.allSettled([publish(), renderManifest()]);
})();`
