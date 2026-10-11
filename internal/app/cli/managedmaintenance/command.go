package managedmaintenance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"
)

// Command runs only on the enrolled application host. The private request is an
// authenticated release-producer handoff; ordinary callers cannot manufacture
// artifact admission by supplying runtime booleans or a mutable image tag.
func Command(ctx context.Context) *cobra.Command {
	command := &cobra.Command{Use: "managed-release", Short: "Maintain a compatible image on an enrolled managed application host"}
	for _, action := range []string{"run", "enroll", "recover", "status", "inspect", "capacity"} {
		var profilePath, requestPath, image, revision string
		child := &cobra.Command{Use: action, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) (result error) {
			defer func() { writeFailureDiagnostic(cmd.ErrOrStderr(), result) }()
			if runtime.GOOS != "linux" || os.Geteuid() != 0 {
				return errors.New("managed release control requires root on the enrolled Linux host")
			}
			var profile HostProfile
			if err := readPrivateJSON(profilePath, &profile); err != nil {
				return err
			}
			if action == "capacity" {
				return capacityDiagnostic(ctx, cmd, profile)
			}
			if err := profile.Validate(); err != nil {
				return err
			}
			for _, path := range []string{profile.Root, profile.AdmissionRoot} {
				if err := privateOperatorRoot(path, false); err != nil {
					return err
				}
			}
			if err := privateOperatorRoot(profile.StateRoot, true); err != nil {
				return err
			}
			if action == "inspect" {
				if !revisionPattern.MatchString(revision) {
					return errors.New("exact source revision required")
				}
				k := &KamalEffects{Profile: profile}
				r := Release{Image: image, Revision: revision}
				bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
				defer cancel()
				img, err := k.image(bounded, image)
				if err != nil {
					return err
				}
				if img.Config.Labels["org.opencontainers.image.revision"] != revision {
					return errors.New("retained image revision mismatch")
				}
				projection, err := k.projection(bounded, r)
				if err != nil {
					return err
				}
				digest, err := ProfileDigest(profile)
				if err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"configurationDigest": digest, "credentialDigest": environmentIdentity(mergeEnvironment(img.Config.Env, projection.Environment))})
			}
			var request Request
			if err := readPrivateJSON(requestPath, &request); err != nil {
				return err
			}
			if err := request.Validate(); err != nil {
				return err
			}
			if err := validateRequestAction(action, request); err != nil {
				return err
			}
			if request.Target != profile.Target {
				return errors.New("request does not bind this managed target")
			}
			if action == "status" {
				var state State
				if err := readPrivateJSON(filepath.Join(profile.StateRoot, JournalName), &state); err != nil {
					return err
				}
				if err := state.Validate(); err != nil {
					return err
				}
				digest, _ := request.Digest()
				if state.RequestDigest != digest || state.Target != profile.Target {
					return errors.New("journal differs from requested operation")
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(state)
			}
			journal, err := OpenJournal(profile.StateRoot, request)
			if err != nil {
				return err
			}
			defer journal.Close()
			coordinator := &Coordinator{Request: request, Journal: journal, Effects: &KamalEffects{Profile: profile, Request: request, lockFile: journal.InheritedLockFile(), recovering: action == "recover"}}
			if action == "recover" {
				return coordinator.Recover(ctx)
			}
			return coordinator.Run(ctx)
		}}
		child.Flags().StringVar(&profilePath, "profile", "", "private enrolled host profile")
		if action != "capacity" {
			child.Flags().StringVar(&requestPath, "request", "", "private authenticated release-producer request")
			child.Flags().StringVar(&image, "image", "", "retained immutable image to inspect")
			child.Flags().StringVar(&revision, "revision", "", "exact source revision to inspect")
		}
		_ = child.MarkFlagRequired("profile")
		if action == "inspect" {
			_ = child.MarkFlagRequired("image")
			_ = child.MarkFlagRequired("revision")
		} else if action != "capacity" {
			_ = child.MarkFlagRequired("request")
		}
		command.AddCommand(child)
	}
	return command
}

func validateRequestAction(action string, request Request) error {
	if action == "enroll" && request.Operation != "enroll" {
		return errors.New("enroll requires an explicit enrollment request")
	}
	if action == "run" && request.Operation == "enroll" {
		return errors.New("initial managed admission requires the enroll command")
	}
	return nil
}

func capacityDiagnostic(ctx context.Context, cmd *cobra.Command, profile HostProfile) error {
	report := CapacityReport{Policy: profile.Capacity}
	err := profile.Validate()
	if err == nil {
		err = privateOperatorRoot(profile.Root, false)
	}
	if err == nil {
		err = privateOperatorRoot(profile.StateRoot, false)
	}
	if err == nil {
		bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		report, err = (&KamalEffects{Profile: profile}).Capacity(bounded)
	}
	if err != nil {
		report.Error = err.Error()
	}
	return errors.Join(err, json.NewEncoder(cmd.OutOrStdout()).Encode(report))
}
func readPrivateJSON(path string, out any) error {
	if !filepath.IsAbs(path) {
		return errors.New("absolute private input path required")
	}
	raw, err := readOperatorFile(path)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0077 != 0 {
		return errors.New("managed input must be private")
	}
	return strictJSON(raw, out)
}
