package composectl

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/creachadair/jrpc2"
	"github.com/creachadair/jrpc2/handler"
)

type QualificationClientWorkerOptions struct {
	Target          string
	Project         string
	SourceRevision  string
	KeyringPassword string
}

type qualificationLoginChallenge struct {
	VerificationURL string `json:"verificationUrl"`
	UserCode        string `json:"userCode"`
}

func parseQualificationCandidate(output, sourceRevision string) (QualificationCandidate, error) {
	return parseQualificationCandidateWithPlan(output, sourceRevision, true)
}

// parseQualificationCandidateBootstrap accepts the candidate projection emitted
// before the first serving generation exists. Bootstrap synchronization still
// prepares the target-owned candidate, but deliberately does not resolve a
// delivery plan until the first generation is active.
func parseQualificationCandidateBootstrap(output, sourceRevision string) (QualificationCandidate, error) {
	return parseQualificationCandidateWithPlan(output, sourceRevision, false)
}

func parseQualificationCandidateWithPlan(output, sourceRevision string, requirePlan bool) (QualificationCandidate, error) {
	var wire struct {
		SchemaVersion    int    `json:"schemaVersion"`
		CandidateID      string `json:"candidateId"`
		Revision         int64  `json:"revision"`
		TargetID         string `json:"targetId"`
		PrincipalID      string `json:"principalId"`
		ArtifactDigest   string `json:"artifactDigest"`
		ProvenanceDigest string `json:"provenanceDigest"`
		PreviewURL       string `json:"previewUrl"`
		PlanID           string `json:"planId"`
		PlanDigest       string `json:"planDigest"`
	}
	if err := json.Unmarshal([]byte(output), &wire); err != nil {
		return QualificationCandidate{}, fmt.Errorf("decode dev result: %w", err)
	}
	if wire.SchemaVersion != 1 {
		return QualificationCandidate{}, fmt.Errorf("unsupported dev result schema %d", wire.SchemaVersion)
	}
	result := QualificationCandidate{
		ID: wire.CandidateID, Revision: wire.Revision, TargetID: wire.TargetID,
		PrincipalID: wire.PrincipalID, ArtifactDigest: wire.ArtifactDigest,
		ProvenanceDigest: wire.ProvenanceDigest, PreviewURL: wire.PreviewURL,
		SourceRevision: strings.TrimSpace(sourceRevision),
		PlanID:         wire.PlanID, PlanDigest: wire.PlanDigest,
	}
	if result.ID == "" || result.Revision <= 0 || result.TargetID == "" ||
		result.PrincipalID == "" || result.PreviewURL == "" {
		return QualificationCandidate{}, fmt.Errorf("incomplete candidate output")
	}
	if requirePlan || result.PlanID != "" || result.PlanDigest != "" {
		if result.PlanID == "" || result.PlanDigest == "" {
			return QualificationCandidate{}, fmt.Errorf("incomplete candidate output")
		}
	}
	for name, value := range map[string]string{
		"artifact digest":   result.ArtifactDigest,
		"provenance digest": result.ProvenanceDigest,
	} {
		if !strings.HasPrefix(value, "sha256:") || len(value) != 71 {
			return QualificationCandidate{}, fmt.Errorf("invalid %s %q", name, value)
		}
	}
	if result.PlanDigest != "" && (!strings.HasPrefix(result.PlanDigest, "sha256:") || len(result.PlanDigest) != 71) {
		return QualificationCandidate{}, fmt.Errorf("invalid plan digest %q", result.PlanDigest)
	}
	return result, nil
}

func parseQualificationPublication(
	output string,
	candidate QualificationCandidate,
) (QualificationPublication, error) {
	var wire struct {
		SchemaVersion int    `json:"schemaVersion"`
		PublicationID string `json:"publicationId"`
		CandidateID   string `json:"candidateId"`
		GenerationID  string `json:"generationId"`
		PlanID        string `json:"planId"`
		PlanDigest    string `json:"planDigest"`
		Status        string `json:"status"`
	}
	if err := json.Unmarshal([]byte(output), &wire); err != nil {
		return QualificationPublication{}, fmt.Errorf("decode publish result: %w", err)
	}
	if wire.SchemaVersion != 1 {
		return QualificationPublication{}, fmt.Errorf("unsupported publish result schema %d", wire.SchemaVersion)
	}
	if wire.PublicationID == "" || wire.GenerationID == "" ||
		wire.PlanID == "" || wire.PlanDigest == "" || wire.Status == "" ||
		wire.CandidateID == "" {
		return QualificationPublication{}, fmt.Errorf("incomplete publication output")
	}
	if wire.CandidateID != candidate.ID || wire.PlanID != candidate.PlanID ||
		wire.PlanDigest != candidate.PlanDigest {
		return QualificationPublication{}, fmt.Errorf("publication output does not match the previewed candidate")
	}
	if wire.Status != "pending" && wire.Status != "committed" {
		return QualificationPublication{}, fmt.Errorf("unsupported publication status %q", wire.Status)
	}
	return QualificationPublication{
		CandidateID: wire.CandidateID, CandidateRevision: candidate.Revision,
		TargetID: candidate.TargetID, PrincipalID: candidate.PrincipalID,
		ArtifactDigest: candidate.ArtifactDigest,
		ReleaseDigest:  candidate.ProvenanceDigest,
		SourceRevision: candidate.SourceRevision,
		DeploymentID:   wire.PublicationID, GenerationID: wire.GenerationID,
		PlanID: wire.PlanID, PlanDigest: wire.PlanDigest, Status: wire.Status,
	}, nil
}

func (c *Controller) RunQualificationClientWorker(
	ctx context.Context,
	options QualificationClientWorkerOptions,
) error {
	options.Target = strings.TrimSpace(options.Target)
	options.Project = strings.TrimSpace(options.Project)
	options.SourceRevision = strings.TrimSpace(options.SourceRevision)
	options.KeyringPassword = strings.TrimSpace(options.KeyringPassword)
	if options.Target == "" || options.Project == "" || options.KeyringPassword == "" {
		return fmt.Errorf("qualification client worker requires target, project, and keyring password")
	}
	runtimeDir, err := os.MkdirTemp("", "leapview-qualification-keyring-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(runtimeDir)
	if err := os.Chmod(runtimeDir, 0o700); err != nil {
		return err
	}
	environment := append(os.Environ(), "XDG_RUNTIME_DIR="+runtimeDir)
	environment, err = startQualificationKeyring(ctx, environment, options.KeyringPassword)
	if err != nil {
		return err
	}

	var currentCandidate QualificationCandidate
	server := jrpc2.NewServer(handler.Map{
		"login": handler.New(func(callCtx context.Context) (map[string]bool, error) {
			err := runQualificationLogin(
				callCtx,
				environment,
				options,
				func(challenge qualificationLoginChallenge) error {
					_, err := jrpc2.ServerFromContext(callCtx).Callback(
						callCtx,
						"device_challenge",
						challenge,
					)
					return err
				},
			)
			return map[string]bool{"authenticated": err == nil}, err
		}),
		"dev": handler.New(func(callCtx context.Context) (QualificationCandidate, error) {
			output, err := runQualificationCLI(
				callCtx,
				environment,
				"dev",
				qualificationDevArguments(options)...,
			)
			if err != nil {
				return QualificationCandidate{}, err
			}
			currentCandidate, err = parseQualificationCandidate(output, options.SourceRevision)
			return currentCandidate, err
		}),
		"publish": handler.New(func(callCtx context.Context) (QualificationPublication, error) {
			if currentCandidate.ID == "" {
				return QualificationPublication{}, fmt.Errorf("qualification candidate is unavailable; run dev first")
			}
			output, err := runQualificationCLI(
				callCtx,
				environment,
				"publish",
				currentCandidate.ID,
				"--format", "json",
			)
			if err != nil {
				return QualificationPublication{}, err
			}
			return parseQualificationPublication(output, currentCandidate)
		}),
	}, &jrpc2.ServerOptions{
		AllowPush:   true,
		Concurrency: 1,
		NewContext:  func() context.Context { return ctx },
	}).Start(newQualificationRPCChannel(
		c.stdin,
		qualificationOutputWriteCloser{Writer: c.stdout},
	))
	if err := server.Wait(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("qualification client worker: %w", err)
	}
	return ctx.Err()
}

type qualificationOutputWriteCloser struct{ io.Writer }

func (qualificationOutputWriteCloser) Close() error { return nil }

func qualificationDevArguments(options QualificationClientWorkerOptions) []string {
	arguments := []string{
		"--once",
		"--no-browser",
		"--project", options.Project,
		"--target", options.Target,
		"--format", "json",
	}
	if options.SourceRevision != "" {
		arguments = append(arguments, "--source-revision", options.SourceRevision)
	}
	return arguments
}

func startQualificationKeyring(
	ctx context.Context,
	environment []string,
	password string,
) ([]string, error) {
	unlock := exec.CommandContext(ctx, "gnome-keyring-daemon", "--unlock")
	unlock.Env = environment
	unlock.Stdin = strings.NewReader(password)
	unlockOutput, err := unlock.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("unlock qualification keyring: %w: %s", err, unlockOutput)
	}
	environment = mergeQualificationEnvironment(environment, string(unlockOutput))

	start := exec.CommandContext(ctx, "gnome-keyring-daemon", "--start", "--components=secrets")
	start.Env = environment
	startOutput, err := start.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("start qualification keyring: %w: %s", err, startOutput)
	}
	return mergeQualificationEnvironment(environment, string(startOutput)), nil
}

func mergeQualificationEnvironment(environment []string, output string) []string {
	values := make(map[string]string, len(environment))
	order := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if _, exists := values[name]; !exists {
			order = append(order, name)
		}
		values[name] = value
	}
	for _, line := range strings.Split(output, "\n") {
		name, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found || name == "" || strings.ContainsAny(name, " \t") {
			continue
		}
		value = strings.Trim(value, "'\"")
		if _, exists := values[name]; !exists {
			order = append(order, name)
		}
		values[name] = value
	}
	result := make([]string, 0, len(order))
	for _, name := range order {
		result = append(result, name+"="+values[name])
	}
	return result
}

func runQualificationLogin(
	ctx context.Context,
	environment []string,
	options QualificationClientWorkerOptions,
	notify func(qualificationLoginChallenge) error,
) error {
	command := exec.CommandContext(
		ctx,
		"leapview",
		"login",
		options.Target,
		"--project", options.Project,
		"--no-browser",
		"--format", "json",
	)
	command.Env = environment
	var output bytes.Buffer
	reader, writer := io.Pipe()
	command.Stdout = io.MultiWriter(&output, writer)
	command.Stderr = io.MultiWriter(&output, writer)
	if err := command.Start(); err != nil {
		_ = writer.Close()
		return fmt.Errorf("start leapview login: %w", err)
	}
	scanned := make(chan error, 1)
	go func() {
		defer close(scanned)
		scanner := bufio.NewScanner(reader)
		challengeSent := false
		authenticated := false
		for scanner.Scan() {
			var event struct {
				SchemaVersion   int    `json:"schemaVersion"`
				Type            string `json:"type"`
				VerificationURL string `json:"verificationUrl"`
				UserCode        string `json:"userCode"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
				scanned <- fmt.Errorf("decode login event: %w", err)
				return
			}
			if event.SchemaVersion != 1 {
				scanned <- fmt.Errorf("unsupported login event schema %d", event.SchemaVersion)
				return
			}
			switch event.Type {
			case "deviceChallenge":
				if challengeSent || event.VerificationURL == "" || event.UserCode == "" {
					scanned <- fmt.Errorf("invalid device challenge event")
					return
				}
				challengeSent = true
				if err := notify(qualificationLoginChallenge{
					VerificationURL: event.VerificationURL,
					UserCode:        event.UserCode,
				}); err != nil {
					scanned <- err
					return
				}
			case "authenticated":
				authenticated = true
			default:
				scanned <- fmt.Errorf("unexpected login event %q", event.Type)
				return
			}
		}
		if !challengeSent || !authenticated {
			scanned <- fmt.Errorf("login event stream is incomplete")
			return
		}
		scanned <- scanner.Err()
	}()
	waitErr := command.Wait()
	_ = writer.Close()
	scanErr := <-scanned
	_ = reader.Close()
	if scanErr != nil {
		return fmt.Errorf("read leapview login: %w", scanErr)
	}
	if waitErr != nil {
		return fmt.Errorf("leapview login: %w: %s", waitErr, redactQualificationLog(output.Bytes(), 100))
	}
	return nil
}

func runQualificationCLI(
	ctx context.Context,
	environment []string,
	commandName string,
	arguments ...string,
) (string, error) {
	command := exec.CommandContext(ctx, "leapview", append([]string{commandName}, arguments...)...)
	command.Env = environment
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf(
			"leapview %s: %w: %s",
			commandName,
			err,
			redactQualificationLog(output, 100),
		)
	}
	return string(output), nil
}
