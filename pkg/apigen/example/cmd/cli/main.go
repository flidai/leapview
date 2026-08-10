package main

import (
	"log"
	"os"
	"strings"

	cobraruntime "github.com/Yacobolo/toolbelt/apigen/runtime/cobra"
	"github.com/spf13/cobra"

	"github.com/example/apigen-example/cmd/cli/gen"
)

func main() {
	baseURL := getenv("TODO_EXAMPLE_BASE_URL", "http://127.0.0.1:8081")
	client := cobraruntime.NewClient(baseURL, "", "")

	root := &cobra.Command{
		Use:   "todo-example",
		Short: "APIGen todo example CLI",
		PersistentPreRun: func(cmd *cobra.Command, _ []string) {
			client.BaseURL = strings.TrimRight(baseURL, "/")
		},
	}
	root.PersistentFlags().StringVar(&baseURL, "base-url", baseURL, "Todo example server URL")

	if err := cobraruntime.AddGeneratedCommands(root, client, gen.APIGeneratedCommandSpecs, cobraruntime.RuntimeOptions{}); err != nil {
		log.Fatal(err)
	}
	if err := root.Execute(); err != nil {
		log.Fatal(err)
	}
}

func getenv(key string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
