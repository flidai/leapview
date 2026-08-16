package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/flidai/leapview/internal/project/devloop"
	"github.com/flidai/leapview/internal/project/schema"
	"github.com/spf13/cobra"
)

type DevOptions struct {
	ProjectPath       string
	Credentials       cliapi.Credentials
	UploadConcurrency int
	Once              bool
	NoBrowser         bool
	CandidateKey      string
	SourceRevision    devloop.SourceRevision
	Format            string
}

// DevRemoteFactory is the Project-owned port for binding the dev loop to an
// authenticated target. The application adapter may implement it with
// Deployment APIs without making Project depend on Deployment.
type DevRemoteFactory interface {
	Remote(
		context.Context,
		cliapi.Credentials,
		int,
	) (devloop.Remote, error)
}

// DevCommand synchronizes coherent local project snapshots into one private
// target candidate. It never starts or embeds a LeapView server.
func DevCommand(
	ctx context.Context,
	client cliapi.Client,
	checkpoints *CandidateCheckpointStore,
	remotes DevRemoteFactory,
	openBrowser func(string) error,
) *cobra.Command {
	values := DevOptions{
		ProjectPath:       filepath.Join("dashboards", "leapview.yaml"),
		UploadConcurrency: 4,
		CandidateKey:      "default",
		Format:            "text",
	}
	command := &cobra.Command{
		Use:   "dev [project]",
		Short: "Synchronize a project into your private target candidate",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if len(args) == 1 {
				if command.Flags().Changed("project") {
					return fmt.Errorf(
						"choose either --project or positional project, not both",
					)
				}
				values.ProjectPath = args[0]
			}
			return RunDev(
				ctx,
				client,
				checkpoints,
				remotes,
				values,
				openBrowser,
				command.OutOrStdout(),
				command.ErrOrStderr(),
			)
		},
	}
	command.Flags().StringVar(
		&values.ProjectPath,
		"project",
		values.ProjectPath,
		"project manifest path",
	)
	command.Flags().StringVar(
		&values.Credentials.Target,
		"target",
		"",
		"authenticated target profile or LeapView target URL",
	)
	command.Flags().StringVar(
		&values.Credentials.Token,
		"token",
		"",
		"ephemeral API token compatibility path",
	)
	command.Flags().IntVar(
		&values.UploadConcurrency,
		"upload-concurrency",
		values.UploadConcurrency,
		"maximum parallel content-addressed source uploads (1-16)",
	)
	command.Flags().BoolVar(
		&values.Once,
		"once",
		false,
		"synchronize one candidate and exit",
	)
	command.Flags().BoolVar(
		&values.NoBrowser,
		"no-browser",
		false,
		"print the private preview URL without opening a system browser",
	)
	command.Flags().StringVar(
		&values.CandidateKey,
		"candidate-key",
		values.CandidateKey,
		"stable authoring session key for branch or change reconciliation",
	)
	command.Flags().StringVar(
		&values.SourceRevision.Revision,
		"source-revision",
		"",
		"exact vendor-neutral source revision to bind as release evidence",
	)
	command.Flags().StringVar(
		&values.SourceRevision.Repository,
		"source-repository",
		"",
		"credential-free source repository identity",
	)
	command.Flags().StringVar(
		&values.SourceRevision.Ref,
		"source-ref",
		"",
		"source ref associated with the revision",
	)
	command.Flags().StringVar(
		&values.SourceRevision.ChangeID,
		"source-change",
		"",
		"change or review identity associated with the revision",
	)
	command.Flags().StringVar(
		&values.Format,
		"format",
		values.Format,
		"output format: text or json",
	)
	return command
}

type DevResult struct {
	SchemaVersion    int    `json:"schemaVersion"`
	CandidateID      string `json:"candidateId"`
	Revision         int64  `json:"revision"`
	TargetID         string `json:"targetId"`
	Environment      string `json:"environment"`
	PrincipalID      string `json:"principalId"`
	ArtifactDigest   string `json:"artifactDigest"`
	ProvenanceDigest string `json:"provenanceDigest"`
	PreviewURL       string `json:"previewUrl"`
}

// RunDev executes the Project-owned candidate synchronization lifecycle. It is
// shared by the public command and target bootstrap adapters that must exercise
// the exact same candidate contract.
func RunDev(
	ctx context.Context,
	client cliapi.Client,
	checkpoints *CandidateCheckpointStore,
	remotes DevRemoteFactory,
	options DevOptions,
	openBrowser func(string) error,
	out,
	errOut io.Writer,
) error {
	if client == nil {
		return fmt.Errorf("Project CLI API client is required")
	}
	if checkpoints == nil {
		return fmt.Errorf("Project candidate checkpoint store is required")
	}
	if remotes == nil {
		return fmt.Errorf("Project candidate remote factory is required")
	}
	if options.Format != "text" && options.Format != "json" {
		return fmt.Errorf("dev format must be text or json")
	}
	credentials, err := client.Resolve(ctx, options.Credentials)
	if err != nil {
		return err
	}
	remote, err := remotes.Remote(
		ctx,
		credentials,
		options.UploadConcurrency,
	)
	if err != nil {
		return err
	}
	sourceRevision, err := devSourceRevision(options.SourceRevision)
	if err != nil {
		return err
	}
	service, err := devloop.New(
		devloop.FilesystemBuilder{
			ProjectPath: options.ProjectPath, SourceRevision: sourceRevision,
			CandidateKey: options.CandidateKey,
		},
		remote,
	)
	if err != nil {
		return err
	}
	lastPreviewURL := ""
	report := func(update devloop.Update) error {
		if update.Err != nil {
			for _, diagnostic := range configschema.Diagnostics(update.Err) {
				fmt.Fprintln(errOut, diagnostic.String())
			}
			return update.Err
		}
		if update.Result.Status != devloop.StatusSynchronized {
			return nil
		}
		candidate := update.Result.Candidate
		if err := validateCandidatePreviewURL(
			credentials.Target,
			candidate.ID,
			candidate.PreviewURL,
		); err != nil {
			return err
		}
		if err := checkpoints.Save(CandidateCheckpoint{
			ProjectPath: options.ProjectPath, TargetOrigin: credentials.Target,
			TargetID: candidate.TargetID, Environment: candidate.Environment,
			ProjectID: candidate.ProjectID.String(), CandidateID: candidate.ID,
			CandidateKey:      options.CandidateKey,
			CandidateRevision: candidate.Revision,
			ArtifactDigest:    candidate.ArtifactDigest,
			ProvenanceDigest:  candidate.ProvenanceDigest,
		}); err != nil {
			return fmt.Errorf("persist publish candidate: %w", err)
		}
		if options.Format == "json" {
			if err := json.NewEncoder(out).Encode(DevResult{
				SchemaVersion:    1,
				CandidateID:      candidate.ID,
				Revision:         candidate.Revision,
				TargetID:         candidate.TargetID,
				Environment:      candidate.Environment,
				PrincipalID:      candidate.OwnerID,
				ArtifactDigest:   candidate.ArtifactDigest,
				ProvenanceDigest: candidate.ProvenanceDigest,
				PreviewURL:       candidate.PreviewURL,
			}); err != nil {
				return fmt.Errorf("write dev result: %w", err)
			}
		} else {
			fmt.Fprintf(out, "synchronized %s\n", candidate.ArtifactDigest)
			fmt.Fprintf(out, "provenance %s\n", candidate.ProvenanceDigest)
			fmt.Fprintf(
				out,
				"candidate %s revision %d target %s environment %s principal %s\n",
				candidate.ID,
				candidate.Revision,
				candidate.TargetID,
				candidate.Environment,
				candidate.OwnerID,
			)
		}
		if candidate.PreviewURL != "" &&
			candidate.PreviewURL != lastPreviewURL {
			if options.Format == "text" {
				fmt.Fprintf(out, "preview %s\n", candidate.PreviewURL)
			}
			if !options.NoBrowser && openBrowser != nil {
				if err := openBrowser(candidate.PreviewURL); err != nil {
					fmt.Fprintf(
						errOut,
						"could not open preview in the system browser: %v; open %s manually\n",
						err,
						candidate.PreviewURL,
					)
				}
			}
			lastPreviewURL = candidate.PreviewURL
		}
		return nil
	}
	if options.Once {
		result, reconcileErr := service.Reconcile(ctx)
		reportErr := report(devloop.Update{
			Result: result,
			Err:    reconcileErr,
		})
		if reconcileErr != nil {
			return reconcileErr
		}
		return reportErr
	}
	watcher, err := devloop.NewWatcher(options.ProjectPath, service)
	if err != nil {
		return err
	}
	signalContext, stop := signal.NotifyContext(
		ctx,
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()
	return watcher.Run(signalContext, func(update devloop.Update) {
		if err := report(update); err != nil && update.Err == nil {
			fmt.Fprintln(errOut, err)
		}
	})
}

func validateCandidatePreviewURL(
	target,
	candidateID,
	previewURL string,
) error {
	targetURL, err := url.Parse(strings.TrimSpace(target))
	if err != nil ||
		(targetURL.Scheme != "http" && targetURL.Scheme != "https") ||
		targetURL.Host == "" ||
		targetURL.User != nil ||
		targetURL.RawQuery != "" ||
		targetURL.Fragment != "" ||
		(targetURL.EscapedPath() != "" && targetURL.EscapedPath() != "/") {
		return fmt.Errorf("resolved target has no canonical HTTP origin")
	}
	candidateID = strings.TrimSpace(candidateID)
	if candidateID == "" {
		return fmt.Errorf("target candidate identity is missing")
	}
	expected := targetURL.Scheme + "://" + targetURL.Host +
		"/candidates/" + url.PathEscape(candidateID)
	if strings.TrimSpace(previewURL) != expected {
		return fmt.Errorf(
			"target returned a non-canonical or state-bearing candidate preview URL",
		)
	}
	return nil
}

func devSourceRevision(
	value devloop.SourceRevision,
) (*devloop.SourceRevision, error) {
	value.Revision = strings.TrimSpace(value.Revision)
	value.Repository = strings.TrimSpace(value.Repository)
	value.Ref = strings.TrimSpace(value.Ref)
	value.ChangeID = strings.TrimSpace(value.ChangeID)
	if value.Revision == "" {
		if value.Repository != "" || value.Ref != "" || value.ChangeID != "" {
			return nil, fmt.Errorf(
				"--source-revision is required when source evidence is supplied",
			)
		}
		return nil, nil
	}
	return &value, nil
}
