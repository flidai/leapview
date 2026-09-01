package composectl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	qualificationEvidenceSchema = 1
	qualificationCleanupTimeout = 2 * time.Minute
)

var (
	qualificationBearerPattern = regexp.MustCompile(`(?i)(Authorization: Bearer )[A-Za-z0-9._~+/-]+`)
	qualificationSecretPattern = regexp.MustCompile(
		`(?i)(accessToken|refreshToken|publisherToken|workloadToken|projectDataToken|recoveryControlToken|auditToken|temporaryPassword|qualificationPassword|password|clientSecret|apiKey|token)"\s*:\s*"[^"]+"`,
	)
	qualificationEnvSecretPattern = regexp.MustCompile(
		`(?i)((?:[A-Z0-9_]*(?:TOKEN|PASSWORD|SECRET|API_KEY)[A-Z0-9_]*)=)[^\s]+`,
	)
)

func newQualificationLoopbackRequest(
	ctx context.Context,
	method string,
	endpoint string,
	body io.Reader,
) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	applyQualificationLoopbackHost(request)
	return request, nil
}

func applyQualificationLoopbackHost(request *http.Request) {
	if address := net.ParseIP(request.URL.Hostname()); address != nil && address.IsLoopback() {
		request.Host = "localhost"
	}
}

type QualificationImageOptions struct {
	Image            string
	EvidenceDir      string
	RequireImmutable bool
}

type QualificationSiteImageOptions struct {
	Image string
}

type QualificationInstalledOptions struct {
	Bundle       string
	EvidenceDir  string
	AllowLocal   bool
	MinFreeBytes int64
}

type QualificationCandidate struct {
	ID               string `json:"candidateId"`
	Revision         int64  `json:"revision"`
	TargetID         string `json:"targetId"`
	PrincipalID      string `json:"principalId"`
	ArtifactDigest   string `json:"artifactDigest"`
	ProvenanceDigest string `json:"provenanceDigest"`
	SourceRevision   string `json:"sourceRevision"`
	PreviewURL       string `json:"previewUrl,omitempty"`
	PlanID           string `json:"planId,omitempty"`
	PlanDigest       string `json:"planDigest,omitempty"`
}

type QualificationPublication struct {
	CandidateID       string `json:"candidateId"`
	CandidateRevision int64  `json:"candidateRevision"`
	TargetID          string `json:"targetId"`
	PrincipalID       string `json:"principalId"`
	ArtifactDigest    string `json:"artifactDigest"`
	ReleaseDigest     string `json:"releaseDigest"`
	SourceRevision    string `json:"sourceRevision"`
	DeploymentID      string `json:"deploymentId,omitempty"`
	GenerationID      string `json:"generationId,omitempty"`
	PlanID            string `json:"planId,omitempty"`
	PlanDigest        string `json:"planDigest,omitempty"`
	Status            string `json:"status"`
}

type QualificationDeployment struct {
	CandidateID       string `json:"candidateId"`
	CandidateRevision int64  `json:"candidateRevision"`
	TargetID          string `json:"targetId"`
	PrincipalID       string `json:"principalId"`
	ArtifactDigest    string `json:"artifactDigest"`
	ReleaseDigest     string `json:"releaseDigest"`
	GenerationID      string `json:"generationId,omitempty"`
	PlanID            string `json:"planId,omitempty"`
	PlanDigest        string `json:"planDigest,omitempty"`
	Status            string `json:"status"`
}

type qualificationFailureCode string

const (
	qualificationFailureCanceled qualificationFailureCode = "CANCELED"
	qualificationFailureTimeout  qualificationFailureCode = "TIMEOUT"
)

type qualificationPhaseEvidence struct {
	Name              string                   `json:"name"`
	Result            string                   `json:"result"`
	FailureCode       qualificationFailureCode `json:"failureCode,omitempty"`
	StartedAt         string                   `json:"startedAt"`
	DurationMillis    int64                    `json:"durationMillis"`
	TimeoutSeconds    int64                    `json:"timeoutSeconds"`
	CleanupGuaranteed bool                     `json:"cleanupGuaranteed"`
}

type qualificationPhaseError struct {
	Phase string
	Code  qualificationFailureCode
	Err   error
}

func (e *qualificationPhaseError) Error() string {
	return fmt.Sprintf("qualification phase %s failed [%s]: %v", e.Phase, e.Code, e.Err)
}

func (e *qualificationPhaseError) Unwrap() error { return e.Err }

type qualificationPhaseTracker struct {
	now           func() time.Time
	phases        []qualificationPhaseEvidence
	active        int
	activeStarted time.Time
	ctx           context.Context
	cancel        context.CancelFunc
}

func newQualificationPhaseTracker(now func() time.Time) *qualificationPhaseTracker {
	if now == nil {
		now = time.Now
	}
	return &qualificationPhaseTracker{now: now, active: -1}
}

func (t *qualificationPhaseTracker) Begin(
	parent context.Context,
	name string,
	timeout time.Duration,
) context.Context {
	if t.active >= 0 {
		panic("qualification phase already active")
	}
	if timeout <= 0 {
		panic("qualification phase timeout must be positive")
	}
	started := t.now()
	t.phases = append(t.phases, qualificationPhaseEvidence{
		Name:              strings.TrimSpace(name),
		Result:            "running",
		StartedAt:         qualificationStartedAt(started),
		TimeoutSeconds:    int64(timeout.Seconds()),
		CleanupGuaranteed: true,
	})
	t.active = len(t.phases) - 1
	t.activeStarted = started
	t.ctx, t.cancel = context.WithTimeout(parent, timeout)
	return t.ctx
}

func (t *qualificationPhaseTracker) Finish(err error) error {
	if t == nil || t.active < 0 {
		return err
	}
	index := t.active
	t.active = -1
	if t.cancel != nil {
		defer t.cancel()
	}
	phase := &t.phases[index]
	phase.DurationMillis = max(0, t.now().Sub(t.activeStarted).Milliseconds())
	phase.Result = "success"
	if err == nil {
		return nil
	}
	phase.Result = "failure"
	code := qualificationPhaseFailureCode(phase.Name)
	if errors.Is(err, context.Canceled) ||
		(t.ctx != nil && errors.Is(t.ctx.Err(), context.Canceled)) {
		code = qualificationFailureCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) ||
		(t.ctx != nil && errors.Is(t.ctx.Err(), context.DeadlineExceeded)) {
		code = qualificationFailureTimeout
	}
	phase.FailureCode = code
	return &qualificationPhaseError{Phase: phase.Name, Code: code, Err: err}
}

func (t *qualificationPhaseTracker) Evidence() []qualificationPhaseEvidence {
	if t == nil {
		return nil
	}
	return append([]qualificationPhaseEvidence(nil), t.phases...)
}

func qualificationPhaseFailureCode(name string) qualificationFailureCode {
	value := strings.ToUpper(strings.Trim(normalizedQualificationName(name), "-"))
	value = strings.ReplaceAll(value, "-", "_")
	if value == "" {
		value = "QUALIFICATION"
	}
	return qualificationFailureCode(value + "_FAILED")
}

func verifyExactAuthoringCandidate(
	candidate QualificationCandidate,
	publication QualificationPublication,
	deployment QualificationDeployment,
) error {
	if publication.Status != "pending" {
		return fmt.Errorf("publication status %q is not pending", publication.Status)
	}
	if deployment.Status != "active" {
		return fmt.Errorf("generation status %q is not active", deployment.Status)
	}
	for _, check := range []struct {
		name string
		want any
		got  any
	}{
		{name: "published candidate", want: candidate.ID, got: publication.CandidateID},
		{name: "deployed candidate", want: candidate.ID, got: deployment.CandidateID},
		{name: "published revision", want: candidate.Revision, got: publication.CandidateRevision},
		{name: "deployed revision", want: candidate.Revision, got: deployment.CandidateRevision},
		{name: "published target", want: candidate.TargetID, got: publication.TargetID},
		{name: "deployed target", want: candidate.TargetID, got: deployment.TargetID},
		{name: "published plan", want: candidate.PlanID, got: publication.PlanID},
		{name: "deployed plan", want: candidate.PlanID, got: deployment.PlanID},
		{name: "published plan digest", want: candidate.PlanDigest, got: publication.PlanDigest},
		{name: "deployed plan digest", want: candidate.PlanDigest, got: deployment.PlanDigest},
		{name: "deployed generation", want: publication.GenerationID, got: deployment.GenerationID},
		{name: "published principal", want: candidate.PrincipalID, got: publication.PrincipalID},
		{name: "deployed principal", want: candidate.PrincipalID, got: deployment.PrincipalID},
		{name: "published release", want: candidate.ProvenanceDigest, got: publication.ReleaseDigest},
		{name: "deployed release", want: candidate.ProvenanceDigest, got: deployment.ReleaseDigest},
		{name: "deployed artifact", want: publication.ArtifactDigest, got: deployment.ArtifactDigest},
		{name: "source revision", want: candidate.SourceRevision, got: publication.SourceRevision},
	} {
		if check.want != check.got {
			return fmt.Errorf("%s mismatch: got %v, want %v", check.name, check.got, check.want)
		}
	}
	return nil
}

func redactQualificationLog(contents []byte, maxLines int) []byte {
	if maxLines <= 0 {
		return nil
	}
	redacted := redactQualificationBytes(contents)
	lines := bytes.Split(redacted, []byte("\n"))
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	if len(lines) == 0 {
		return nil
	}
	return append(bytes.Join(lines, []byte("\n")), '\n')
}

func redactQualificationBytes(contents []byte) []byte {
	redacted := qualificationBearerPattern.ReplaceAll(contents, []byte(`${1}[REDACTED]`))
	redacted = qualificationSecretPattern.ReplaceAll(
		redacted,
		[]byte(`${1}":"[REDACTED]"`),
	)
	return qualificationEnvSecretPattern.ReplaceAll(
		redacted,
		[]byte(`${1}[REDACTED]`),
	)
}

type qualificationCleanup struct {
	steps []func(context.Context) error
}

func (c *qualificationCleanup) Add(step func(context.Context) error) {
	if step != nil {
		c.steps = append(c.steps, step)
	}
}

func (c *qualificationCleanup) Run(ctx context.Context) error {
	var result error
	for index := len(c.steps) - 1; index >= 0; index-- {
		result = errors.Join(result, c.steps[index](ctx))
	}
	c.steps = nil
	return result
}

func qualificationContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	return context.WithTimeout(parent, timeout)
}

func normalizedQualificationName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if value == "" {
		return "qualification"
	}
	return value
}
