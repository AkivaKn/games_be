package main

import (
	"errors"
	"log"
	"os"
	"path/filepath"

	"boggle-api/internal/app"
	boggleserver "boggle-api/internal/boggle/server"
	millionaireserver "boggle-api/internal/millionaire/server"
	misclueserver "boggle-api/internal/misclue/server"
	outoflineserver "boggle-api/internal/outofline/server"

	"github.com/joho/godotenv"
)

// ONE binary, BOTH games (the M-A1 fold). This entrypoint is deliberately
// thin: everything shared — the DB pool, the gin engine, /health, CORS, the
// rate-limit policy, the origin allowlist — lives in internal/app; each game
// contributes only a module that mounts its own route tree under its prefix.
//
// The build path stays `./cmd/api/main.go`, which is what both the Dockerfile
// and the deploy hook already use — folding the games in must not require
// touching CI.
func findEnvDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, ".env")); err == nil {
			return dir
		}
		if _, err := os.Stat(filepath.Join(dir, ".env.local")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func loadEnvFiles() {
	// Precedence: process env > .env.local > .env
	dir := findEnvDir()
	if dir == "" {
		log.Printf("env: no .env/.env.local found from cwd")
		return
	}
	for _, name := range []string{".env.local", ".env"} {
		path := filepath.Join(dir, name)
		if err := godotenv.Load(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			log.Printf("env: could not load %s: %v", path, err)
			continue
		}
		log.Printf("env: loaded %s", path)
	}
}

func main() {
	loadEnvFiles()
	origins := app.AllowedOrigins()
	db := app.Connect()
	defer db.Close()

	engine := app.NewEngine(origins)
	limiters := app.NewLimiters()

	boggleserver.NewModule(db, origins, limiters).RegisterRoutes(engine)
	millionaireserver.NewModule(db, origins, limiters).RegisterRoutes(engine)
	outoflineserver.NewModule(db, origins, limiters).RegisterRoutes(engine)
	misclueserver.NewModule(limiters).RegisterRoutes(engine)

	addr := app.ListenAddr()
	log.Printf("Gin server starting on port %s (routes: /api/%s, /api/%s, /api/%s, /api/%s)",
		addr, boggleserver.RoutePrefix, millionaireserver.RoutePrefix, outoflineserver.RoutePrefix, misclueserver.RoutePrefix)
	if err := engine.Run(addr); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}
}
