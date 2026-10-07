package composectl

import (
	"context"
	"io"

	"github.com/flidai/leapview/internal/app/cli/installationstate"
)

func (c *Controller) compose(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
	return c.composeWithEnvironment(ctx, nil, stdin, stdout, stderr, args...)
}

func (c *Controller) composeWithEnvironment(ctx context.Context, environment map[string]string, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
	if c.composeOverride != nil {
		return c.composeOverride(ctx, stdin, stdout, stderr, args...)
	}
	commandArgs, err := composeArguments(c.root, args...)
	if err != nil {
		return err
	}
	processEnvironment, err := composeProcessEnvironment(c.root, environment)
	if err != nil {
		return err
	}
	return c.dockerWithEnvironment(ctx, stdin, stdout, stderr, processEnvironment, commandArgs...)
}

func (c *Controller) composeForPhase(
	ctx context.Context,
	phase string,
	environment map[string]string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	args ...string,
) error {
	if c.composeOverride != nil {
		return c.composeOverride(ctx, stdin, stdout, stderr, args...)
	}
	commandArgs, err := composeArgumentsForPhase(c.root, phase, args...)
	if err != nil {
		return err
	}
	effectiveEnvironment := make(map[string]string, len(environment)+4)
	for name, value := range environment {
		effectiveEnvironment[name] = value
	}
	if phase == installationstate.PhasePrivate {
		for name, value := range privateBootstrapEnvironment {
			effectiveEnvironment[name] = value
		}
	}
	processEnvironment, err := composeProcessEnvironment(c.root, effectiveEnvironment)
	if err != nil {
		return err
	}
	return c.dockerWithEnvironment(ctx, stdin, stdout, stderr, processEnvironment, commandArgs...)
}
