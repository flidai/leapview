package main

import (
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/example/apigen-example/internal/api"
)

func main() {
	addr := getenv("TODO_EXAMPLE_ADDR", "127.0.0.1:8081")

	log.Printf("todo example listening on http://%s", addr)
	if err := http.ListenAndServe(addr, api.NewRouter()); err != nil {
		log.Fatal(err)
	}
}

func getenv(key string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
