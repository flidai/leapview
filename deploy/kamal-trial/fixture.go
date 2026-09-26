// A synthetic, non-root HTTP app for deployment lifecycle tests. Not LeapView.
package main

import (
	"encoding/json"
	"net/http"
	"os"
)

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" && os.Getenv("TRIAL_UNHEALTHY") == "1" {
			http.Error(w, "deliberately unhealthy", http.StatusServiceUnavailable)
			return
		}
		version, _ := os.ReadFile("/version")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"version": string(version), "image_reference": os.Getenv("TRIAL_IMAGE_REFERENCE"),
			"host": r.Host, "forwarded_proto": r.Header.Get("X-Forwarded-Proto"),
		})
	})
	if err := http.ListenAndServe(":8081", nil); err != nil {
		panic(err)
	}
}
