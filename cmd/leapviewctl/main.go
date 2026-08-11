package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/flidai/leapview/internal/app/cli/composectl"
	"github.com/flidai/leapview/internal/app/cli/hostinstall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "leapviewctl: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	root := strings.TrimSpace(os.Getenv("LEAPVIEWCTL_ROOT"))
	if root == "" {
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		root = filepath.Dir(executable)
	}
	controller, err := composectl.New(composectl.Options{
		Root:      root,
		DockerBin: os.Getenv("LEAPVIEWCTL_DOCKER_BIN"),
		Stdin:     os.Stdin,
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
	})
	if err != nil {
		return err
	}
	command := composectl.Command(ctx, controller)
	command.AddCommand(hostinstall.Command(ctx, hostinstall.CommandOptions{
		DockerBin: os.Getenv("LEAPVIEWCTL_DOCKER_BIN"),
		Stdin:     os.Stdin,
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
	}))
	return command.Execute()
}
