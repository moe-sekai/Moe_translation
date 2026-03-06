package main

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"sekai-translate/backend"
)

func main() {
	port := envOr("PORT", "9090")
	dataPath := envOr("TRANSLATION_PATH", "./translations")
	accounts := os.Getenv("TRANSLATOR_ACCOUNTS") // "user1:pass1,user2:pass2"
	secret := envOr("AUTH_SECRET", "sekai-translate-secret")
	ghToken := os.Getenv("GITHUB_TOKEN")
	ghRepo := envOr("GITHUB_REPO", "moe-sekai/MoeSekai-Hub")        // the STATIC hosting repo
	ghPath := envOr("GITHUB_PUSH_PATH", "translation")               // path inside moesekai-hub
	ghBranch := envOr("GITHUB_BRANCH", "main")
	autoPush := os.Getenv("AUTO_PUSH_ENABLED") == "true"
	staticDir := envOr("STATIC_DIR", "./proofreading/out") // built proofreading UI

	store := backend.NewStore(dataPath)
	auth := backend.NewAuth(accounts, secret)
	pusher := backend.NewPusher(ghToken, ghRepo, ghPath, ghBranch)

	mux := http.NewServeMux()
	h := backend.NewHandler(store, auth, pusher)
	h.RegisterRoutes(mux)

	// Serve proofreading UI at /translation/editor/ (matches Next.js basePath)
	fs := http.FileServer(http.Dir(staticDir))
	mux.Handle("/translation/editor/", http.StripPrefix("/translation/editor/", fs))
	// Root redirect
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "" {
			http.Redirect(w, r, "/translation/editor/", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})

	if autoPush {
		go func() {
			ticker := time.NewTicker(1 * time.Hour)
			defer ticker.Stop()
			for range ticker.C {
				fmt.Println("[push] Scheduled push starting...")
				if err := pusher.PushAll(store, "scheduled"); err != nil {
					fmt.Printf("[push] Scheduled push failed: %v\n", err)
				} else {
					fmt.Println("[push] Scheduled push completed")
				}
			}
		}()
		fmt.Println("Auto-push enabled (every 1h)")
	}

	// CORS middleware
	handler := corsMiddleware(mux)

	fmt.Printf("sekai-translate server starting on :%s\n", port)
	fmt.Printf("  translations: %s\n", dataPath)
	fmt.Printf("  push target:  %s/%s\n", ghRepo, ghPath)
	if err := http.ListenAndServe(":"+port, handler); err != nil {
		fmt.Fprintf(os.Stderr, "Fatal: %v\n", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
