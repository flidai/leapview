package composectl

import (
	"context"
	"os"

	"github.com/flidai/leapview/internal/platform/buildinfo"
	"github.com/spf13/cobra"
)

func Command(ctx context.Context, controller *Controller) *cobra.Command {
	root := &cobra.Command{
		Use:           "leapviewctl",
		Short:         "Operate one Docker Compose LeapView instance",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(command *cobra.Command, _ []string) error {
			return command.Help()
		},
	}
	root.SetIn(controller.stdin)
	root.SetOut(controller.stdout)
	root.SetErr(controller.stderr)

	versionJSON := false
	version := &cobra.Command{
		Use:   "version",
		Short: "Report the leapviewctl build identity",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return buildinfo.Write(command.OutOrStdout(), "leapviewctl", buildinfo.Current(), versionJSON)
		},
	}
	version.Flags().BoolVar(&versionJSON, "json", false, "emit machine-readable JSON")

	initOptions := InitOptions{Environment: defaultEnvironment}
	initialize := &cobra.Command{
		Use:   "init",
		Short: "Initialize configuration and one-time credentials",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return controller.Initialize(ctx, initOptions)
		},
	}
	initialize.Flags().StringVar(&initOptions.AdminEmail, "admin-email", "", "initial platform administrator email")
	initialize.Flags().StringVar(&initOptions.Domain, "domain", "", "canonical public application hostname")
	initialize.Flags().StringVar(&initOptions.Environment, "environment", defaultEnvironment, "instance environment")
	initialize.Flags().StringVar(&initOptions.Image, "image", "", "immutable LeapView image reference")
	initialize.Flags().BoolVar(&initOptions.NoHTTPS, "no-https", false, "use a trusted external HTTPS proxy instead of the Caddy overlay")

	start := &cobra.Command{
		Use:   "start",
		Short: "Start the instance and wait for health",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return controller.Start(ctx)
		},
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "Show Compose and application health",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return controller.Status(ctx)
		},
	}
	logs := &cobra.Command{
		Use:                "logs [compose log arguments...]",
		Short:              "Show Compose service logs",
		DisableFlagParsing: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return controller.Logs(ctx, args)
		},
	}
	firstLogin := &cobra.Command{
		Use:   "first-login",
		Short: "Print and delete the one-time credential file",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return controller.FirstLogin()
		},
	}

	imageQualification := QualificationImageOptions{}
	qualifyImage := &cobra.Command{
		Use:   "image",
		Short: "Qualify an already-built production image",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return controller.QualifyImage(ctx, imageQualification)
		},
	}
	qualifyImage.Flags().StringVar(&imageQualification.Image, "image", "", "local production image tag to qualify")
	qualifyImage.Flags().StringVar(&imageQualification.EvidenceDir, "evidence-dir", "", "directory for bounded qualification evidence")
	qualifyImage.Flags().BoolVar(&imageQualification.RequireImmutable, "require-immutable", false, "require a repository reference pinned by SHA-256 digest")

	siteImageQualification := QualificationSiteImageOptions{}
	qualifySiteImage := &cobra.Command{
		Use:   "site-image",
		Short: "Qualify an already-built public site image",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return controller.QualifySiteImage(ctx, siteImageQualification)
		},
	}
	qualifySiteImage.Flags().StringVar(&siteImageQualification.Image, "image", "", "public site image tag or immutable digest to qualify")

	installedQualification := QualificationInstalledOptions{}
	qualifyInstalled := &cobra.Command{
		Use:   "installed-candidate",
		Short: "Qualify this extracted immutable release candidate",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return controller.QualifyInstalledCandidate(ctx, installedQualification)
		},
	}
	qualifyInstalled.Flags().StringVar(&installedQualification.Bundle, "bundle", "", "extracted immutable release bundle (defaults to this leapviewctl directory)")
	qualifyInstalled.Flags().StringVar(&installedQualification.EvidenceDir, "evidence-dir", "", "directory for bounded qualification evidence")
	qualifyInstalled.Flags().BoolVar(&installedQualification.AllowLocal, "allow-local-image", false, "allow a local immutable registry reference during development")
	qualifyInstalled.Flags().Int64Var(&installedQualification.MinFreeBytes, "minimum-free-bytes", 0, "local-only managed-data free-space override")

	qualify := &cobra.Command{
		Use:   "qualify",
		Short: "Run typed production release qualification",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return command.Help()
		},
	}
	clientWorkerOptions := QualificationClientWorkerOptions{}
	qualifyClientWorker := &cobra.Command{
		Use:    "client-worker",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			clientWorkerOptions.KeyringPassword = os.Getenv("QUALIFICATION_KEYRING_PASSWORD")
			return controller.RunQualificationClientWorker(ctx, clientWorkerOptions)
		},
	}
	qualifyClientWorker.Flags().StringVar(&clientWorkerOptions.Target, "target", "", "qualification target")
	qualifyClientWorker.Flags().StringVar(&clientWorkerOptions.Project, "project", "", "qualification project")
	qualifyClientWorker.Flags().StringVar(&clientWorkerOptions.SourceRevision, "source-revision", "", "staged source revision")

	qualify.AddCommand(qualifyImage, qualifySiteImage, qualifyInstalled, qualifyClientWorker)

	root.AddCommand(version, initialize, start, status, logs, firstLogin, qualify)
	return root
}
