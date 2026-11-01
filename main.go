package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/kkdai/linebot-helper-go/internal/config"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.LoadFromEnv()
	if err != nil {
		slog.Error("Configuration error", "error", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprint(w, "OK")
	})
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	addr := ":" + cfg.Port
	slog.Info("Starting server", "addr", addr, "project", cfg.GoogleCloudProject, "location", cfg.GoogleCloudLocation)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("Server exited", "error", err)
		os.Exit(1)
	}
}
