package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
	"github.com/flidai/leapview/internal/app/cli/localdocker"
	"github.com/flidai/leapview/internal/app/cli/localruntime"
	"github.com/spf13/cobra"
)

type localRuntimeLifecycle interface {
	Run(context.Context, bool) error
	Status(context.Context) (localruntime.LifecycleStatus, error)
	Logs(context.Context, int) ([]byte, error)
	Stop(context.Context) error
	PlanReset(context.Context) (localruntime.ResetPlan, error)
	Reset(context.Context, string) error
}

type localRuntimeControllerFactory func(localdocker.Endpoint, *cobra.Command) (localRuntimeLifecycle, error)

type localDevelopmentProfileStatusReader func(context.Context, localruntime.LifecycleStatus) (*localruntime.DevelopmentProfileStatus, error)

func newLocalRuntimeController(endpoint localdocker.Endpoint, command *cobra.Command) (localRuntimeLifecycle, error) {
	return newLocalRuntimeControllerWithCredentials(endpoint, command, nil)
}

func newLocalRuntimeControllerWithCredentials(endpoint localdocker.Endpoint, command *cobra.Command, credentials map[string]string) (*localruntime.Controller, error) {
	return newLocalRuntimeControllerForProfile(endpoint, command, credentials, localruntime.DevelopmentProfileIdentity{})
}

func newLocalRuntimeControllerForProfile(endpoint localdocker.Endpoint, command *cobra.Command, credentials map[string]string, profile localruntime.DevelopmentProfileIdentity) (*localruntime.Controller, error) {
	return localruntime.New(localruntime.Options{
		Endpoint: endpoint, ResolveProjectAuthority: resolveLocalProjectAuthority,
		EstablishSessions: func(ctx context.Context, request localruntime.SessionRequest) (localruntime.SessionResult, error) {
			return establishLocalAuthoringSessions(ctx, request, command.OutOrStdout())
		},
		ResetSessions: resetLocalAuthoringSessions,
		Stdout:        command.OutOrStdout(), DevelopmentCredentials: credentials, DevelopmentProfile: profile,
	})
}

func runAttachedLocalRuntime(ctx context.Context, controller localRuntimeLifecycle, once bool) error {
	signalContext, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	return controller.Run(signalContext, once)
}

func addLocalDevLifecycleCommands(ctx context.Context, parent *cobra.Command, resolve localDockerResolver, factory localRuntimeControllerFactory, readProfileStatus localDevelopmentProfileStatusReader) {
	parent.AddCommand(localDevStatusCommand(ctx, resolve, factory, readProfileStatus))
	parent.AddCommand(localDevLogsCommand(ctx, resolve, factory))
	parent.AddCommand(localDevStopCommand(ctx, resolve, factory))
	parent.AddCommand(localDevResetCommand(ctx, resolve, factory))
}

type localDockerSelection struct {
	context string
	host    string
}

func (selection *localDockerSelection) bind(command *cobra.Command) {
	command.Flags().StringVar(&selection.context, "docker-context", "", "explicit local Docker context")
	command.Flags().StringVar(&selection.host, "docker-host", "", "explicit local Docker Unix socket")
}

func (selection localDockerSelection) controller(ctx context.Context, command *cobra.Command, resolve localDockerResolver, factory localRuntimeControllerFactory) (localRuntimeLifecycle, error) {
	if resolve == nil || factory == nil {
		return nil, errors.New("local development lifecycle is not configured")
	}
	endpoint, err := resolve(ctx, localdocker.Options{ExplicitContext: selection.context, ExplicitHost: selection.host})
	if err != nil {
		return nil, err
	}
	return factory(endpoint, command)
}

func localDevStatusCommand(ctx context.Context, resolve localDockerResolver, factory localRuntimeControllerFactory, readProfileStatus localDevelopmentProfileStatusReader) *cobra.Command {
	selection := &localDockerSelection{}
	format := "text"
	command := &cobra.Command{
		Use:   "status",
		Short: "Report this checkout's local runtime and live attachments",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if format != "text" && format != "json" {
				return errors.New("dev status format must be text or json")
			}
			controller, err := selection.controller(ctx, command, resolve, factory)
			if err != nil {
				return err
			}
			status, err := controller.Status(ctx)
			if err != nil {
				return err
			}
			if status.Exists && status.TargetName != "" && readProfileStatus != nil {
				status.DevelopmentProfile, err = readProfileStatus(ctx, status)
				if err != nil {
					return fmt.Errorf("read retained development profile status: %w", err)
				}
			}
			if format == "json" {
				return json.NewEncoder(command.OutOrStdout()).Encode(status)
			}
			writeLocalRuntimeStatus(command, status)
			return nil
		},
	}
	selection.bind(command)
	command.Flags().StringVar(&format, "format", format, "output format: text or json")
	return command
}

func readApplicationDevelopmentProfileStatus(ctx context.Context, status localruntime.LifecycleStatus) (*localruntime.DevelopmentProfileStatus, error) {
	authority, err := defaultAuthoringAuthenticator(http.DefaultClient)
	if err != nil {
		return nil, err
	}
	return readDevelopmentProfileStatusWith(ctx, status, authority, http.DefaultClient)
}

func readDevelopmentProfileStatusWith(ctx context.Context, status localruntime.LifecycleStatus, authority authoringCredentialResolver, client *http.Client) (*localruntime.DevelopmentProfileStatus, error) {
	if status.TargetName == "" || status.TargetID == "" || status.ProjectID == "" || status.URL == "" {
		return nil, errors.New("local runtime target identity is incomplete")
	}
	if authority == nil || client == nil {
		return nil, errors.New("local authoring status authority is unavailable")
	}
	resolved, err := authority.Resolve(ctx, status.TargetName)
	if err != nil {
		return nil, err
	}
	if resolved.Profile.ProjectID != status.ProjectID || resolved.Profile.InstanceID != status.TargetID || resolved.Profile.Origin != status.URL {
		return nil, errors.New("local runtime identity disagrees with the retained authoring login")
	}
	transport := capabilityAPITransport{target: resolved.Profile.Origin, token: resolved.AccessToken, client: client}
	response, err := analyticsgen.NewGenClient(transport).GetDevelopmentProfileApplication(ctx, analyticsgen.GenGetDevelopmentProfileApplicationClientRequest{
		Project: status.ProjectID, Target: status.TargetID,
	})
	if isDevelopmentProfileNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	incomplete := append([]string(nil), response.Body.IncompleteConnections...)
	sort.Strings(incomplete)
	return &localruntime.DevelopmentProfileStatus{
		ApplicationID: response.Body.ApplicationId, Status: string(response.Body.Status), ProfileName: response.Body.ProfileName,
		LastCompletedApplicationID: optionalLocalStatusString(response.Body.LastCompletedApplicationId), LastCompletedAt: optionalLocalStatusString(response.Body.LastCompletedAt),
		RequiredConnections: response.Body.RequiredConnectionCount, AppliedConnections: response.Body.AppliedConnectionCount,
		IncompleteConnections: incomplete, UpdatedAt: response.Body.UpdatedAt,
	}, nil
}

func optionalLocalStatusString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func writeLocalRuntimeStatus(command *cobra.Command, status localruntime.LifecycleStatus) {
	out := command.OutOrStdout()
	if !status.Exists {
		fmt.Fprintf(out, "No local runtime exists for checkout %s (%s).\n", status.CheckoutRoot, status.CheckoutID)
		return
	}
	fmt.Fprintf(out, "Checkout: %s\n", status.CheckoutRoot)
	fmt.Fprintf(out, "Runtime: %s (%s, phase %s)\n", status.ComposeProject, status.RuntimeStatus, status.Phase)
	fmt.Fprintf(out, "Owner: %s\n", status.OwnerID)
	fmt.Fprintf(out, "URL: %s\n", status.URL)
	serviceNames := make([]string, 0, len(status.Services))
	for name := range status.Services {
		serviceNames = append(serviceNames, name)
	}
	sort.Strings(serviceNames)
	for _, name := range serviceNames {
		fmt.Fprintf(out, "Service %s: %s\n", name, status.Services[name])
	}
	if status.DevelopmentProfile == nil {
		fmt.Fprintln(out, "Development profile application: none")
	} else {
		profile := status.DevelopmentProfile
		fmt.Fprintf(out, "Development profile application: %s (%s, profile %s, updated %s)\n", profile.ApplicationID, profile.Status, profile.ProfileName, profile.UpdatedAt)
		if profile.LastCompletedApplicationID == "" {
			fmt.Fprintln(out, "Last completed profile application: none")
		} else {
			fmt.Fprintf(out, "Last completed profile application: %s (%s)\n", profile.LastCompletedApplicationID, profile.LastCompletedAt)
		}
		fmt.Fprintf(out, "Development profile connections: %d/%d applied\n", profile.AppliedConnections, profile.RequiredConnections)
		if len(profile.IncompleteConnections) > 0 {
			fmt.Fprintf(out, "Incomplete connections: %s\n", strings.Join(profile.IncompleteConnections, ", "))
		}
	}
	if len(status.Attachments) == 0 {
		fmt.Fprintln(out, "Attachments: none")
		return
	}
	fmt.Fprintf(out, "Attachments: %d\n", len(status.Attachments))
	for _, attachment := range status.Attachments {
		fmt.Fprintf(out, "- %s pid=%d heartbeat=%s\n", attachment.ID, attachment.PID, attachment.HeartbeatAt.UTC().Format("2006-01-02T15:04:05.000000000Z"))
	}
}

func localDevLogsCommand(ctx context.Context, resolve localDockerResolver, factory localRuntimeControllerFactory) *cobra.Command {
	selection := &localDockerSelection{}
	tail := 200
	command := &cobra.Command{
		Use:   "logs",
		Short: "Read redacted logs from this checkout's local runtime",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if tail < 1 || tail > 10000 {
				return errors.New("local log tail must be between 1 and 10000 lines")
			}
			controller, err := selection.controller(ctx, command, resolve, factory)
			if err != nil {
				return err
			}
			logs, err := controller.Logs(ctx, tail)
			if err != nil {
				return err
			}
			_, err = command.OutOrStdout().Write(logs)
			return err
		},
	}
	selection.bind(command)
	command.Flags().IntVar(&tail, "tail", tail, "maximum log lines per service (1-10000)")
	return command
}

func localDevStopCommand(ctx context.Context, resolve localDockerResolver, factory localRuntimeControllerFactory) *cobra.Command {
	selection := &localDockerSelection{}
	command := &cobra.Command{
		Use:   "stop",
		Short: "Stop this checkout's services when no live attachment remains",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			controller, err := selection.controller(ctx, command, resolve, factory)
			if err != nil {
				return err
			}
			if err := controller.Stop(ctx); err != nil {
				return err
			}
			fmt.Fprintln(command.OutOrStdout(), "Stopped this checkout's local services; persistent data was retained.")
			return nil
		},
	}
	selection.bind(command)
	return command
}

func localDevResetCommand(ctx context.Context, resolve localDockerResolver, factory localRuntimeControllerFactory) *cobra.Command {
	selection := &localDockerSelection{}
	confirmation := ""
	command := &cobra.Command{
		Use:   "reset",
		Short: "Remove the exactly confirmed local state for this checkout",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			controller, err := selection.controller(ctx, command, resolve, factory)
			if err != nil {
				return err
			}
			if strings.TrimSpace(confirmation) == "" {
				plan, err := controller.PlanReset(ctx)
				if err != nil {
					return err
				}
				writeResetPlan(command, plan)
				return fmt.Errorf("reset requires --confirm %s", plan.Confirmation)
			}
			if confirmation != strings.TrimSpace(confirmation) {
				return localruntime.ErrResetConfirmation
			}
			if err := controller.Reset(ctx, confirmation); err != nil {
				return err
			}
			fmt.Fprintln(command.OutOrStdout(), "Removed the confirmed local runtime and persistent state for this checkout.")
			return nil
		},
	}
	selection.bind(command)
	command.Flags().StringVar(&confirmation, "confirm", "", "exact confirmation digest printed by an unconfirmed reset")
	return command
}

func writeResetPlan(command *cobra.Command, plan localruntime.ResetPlan) {
	out := command.OutOrStdout()
	fmt.Fprintf(out, "Checkout to reset: %s\n", plan.CheckoutRoot)
	fmt.Fprintf(out, "Checkout identity: %s\n", plan.CheckoutID)
	if len(plan.Resources) == 0 {
		fmt.Fprintln(out, "Docker resources: none (retained local state will still be removed)")
	} else {
		fmt.Fprintln(out, "Docker resources:")
		for _, resource := range plan.Resources {
			fmt.Fprintf(out, "- %s %s\n", resource.Kind, resource.ID)
		}
	}
	fmt.Fprintf(out, "Exact confirmation: %s\n", plan.Confirmation)
}
