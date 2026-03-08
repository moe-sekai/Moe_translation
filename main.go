package main

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"sekai-translate/backend"
)

func main() {
	port := envOr("PORT", "9090")
	dataPath := envOr("TRANSLATION_PATH", "./translations")
	accounts := os.Getenv("TRANSLATOR_ACCOUNTS") // "user1:pass1,user2:pass2"
	secret := envOr("AUTH_SECRET", "sekai-translate-secret")
	expectedRepo := envOr("SELF_REPO", "moe-sekai/Moe_translation")
	gitRepoURL := os.Getenv("GIT_PUSH_REPO_URL")
	gitBranch := envOr("GIT_PUSH_BRANCH", "backup-translations")
	gitWorkspace := envOr("GIT_WORKSPACE", "/app/git-workspace")
	dataDir := envOr("DATA_DIR", "./data")

	if gitRepoURL == "" {
		token := os.Getenv("GITHUB_TOKEN")
		repo := envOr("GITHUB_REPO", "moe-sekai/Moe_translation")
		if token != "" {
			gitRepoURL = fmt.Sprintf("https://x-access-token:%s@github.com/%s.git", token, repo)
		}
	}

	if gitRepoURL == "" {
		fmt.Fprintf(os.Stderr, "Fatal: GIT_PUSH_REPO_URL is required\n")
		os.Exit(1)
	}
	if !isAllowedPushTarget(gitRepoURL, expectedRepo) {
		fmt.Fprintf(os.Stderr, "Fatal: push target must be current repo (%s), got %s\n", expectedRepo, maskURL(gitRepoURL))
		os.Exit(1)
	}

	llmType := envOr("LLM_TYPE", "gemini")
	upstreamRepo := envOr("UPSTREAM_REPO", "Team-Haruki/haruki-sekai-master")
	upstreamBranch := envOr("UPSTREAM_BRANCH", "main")
	schedulerEnabled := envOr("TRANSLATE_SCHEDULER_ENABLED", "true") == "true"
	staticDir := envOr("STATIC_DIR", "./proofreading/out") // built proofreading UI

	store := backend.NewStore(dataPath)
	auth := backend.NewAuth(accounts, secret)
	pusher := backend.NewPusher(gitRepoURL, gitBranch, gitWorkspace, dataPath)
	translator := backend.NewTranslator(store, backend.TranslatorConfig{
		LLMType:        llmType,
		GeminiAPIKey:   os.Getenv("GEMINI_API_KEY"),
		GeminiModel:    envOr("GEMINI_MODEL", "gemini-2.0-flash"),
		OpenAIAPIKey:   os.Getenv("OPENAI_API_KEY"),
		OpenAIBaseURL:  envOr("OPENAI_BASE_URL", "https://api.openai.com/v1"),
		OpenAIModel:    envOr("OPENAI_MODEL", "gpt-4o-mini"),
		BatchSize:      envIntOr("TRANSLATE_BATCH_SIZE", 20),
		RateLimitDelay: envDurationMsOr("TRANSLATE_RATE_DELAY_MS", 800),
	})
	scheduler := backend.NewScheduler(translator, pusher, store, upstreamRepo, upstreamBranch, schedulerEnabled)
	scheduler.Start()
	searchIndex := backend.NewSearchIndexBuilder(
		store,
		dataDir,
		envDurationMsOr("SEARCH_INDEX_DEBOUNCE_MS", 3600000),
		envDurationMsOr("SEARCH_INDEX_REFRESH_MS", 3600000),
	)
	searchIndex.Start()

	mux := http.NewServeMux()
	h := backend.NewHandler(store, auth, pusher, translator, scheduler)
	h.RegisterRoutes(mux)

	// Serve proofreading UI at /translation/editor/ (matches Next.js basePath)
	fs := http.FileServer(http.Dir(staticDir))
	mux.Handle("/translation/editor/", http.StripPrefix("/translation/editor/", fs))

	// Serve translation JSON files at /translation/ (matches dataPath)
	dataFs := http.FileServer(http.Dir(dataPath))
	mux.Handle("/translation/", http.StripPrefix("/translation/", dataFs))

	// Serve search index and related data at /data/
	dataOutFs := http.FileServer(http.Dir(dataDir))
	mux.Handle("/data/", http.StripPrefix("/data/", dataOutFs))

	// Root redirect
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "" {
			http.Redirect(w, r, "/translation/editor/", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})

	// CORS middleware
	handler := corsMiddleware(mux)

	fmt.Printf("sekai-translate server starting on :%s\n", port)
	fmt.Printf("  translations: %s\n", dataPath)
	fmt.Printf("  data dir:     %s\n", dataDir)
	fmt.Printf("  push target:  %s\n", maskURL(gitRepoURL))
	fmt.Printf("  upstream:     %s@%s (enabled=%v)\n", upstreamRepo, upstreamBranch, schedulerEnabled)
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

func envIntOr(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func envDurationMsOr(key string, fallback int) time.Duration {
	v := envIntOr(key, fallback)
	if v <= 0 {
		v = fallback
	}
	return time.Duration(v) * time.Millisecond
}

func maskURL(url string) string {
	if strings.Contains(url, "@github.com") && strings.Contains(url, "https://") {
		start := strings.Index(url, "https://")
		at := strings.Index(url[start:], "@github.com")
		if start >= 0 && at > 0 {
			at = start + at
			return url[:start+8] + "***" + url[at:]
		}
	}
	return url
}

func isAllowedPushTarget(repoURL, expectedRepo string) bool {
	normalizedURL := strings.ToLower(strings.TrimSpace(repoURL))
	normalizedExpected := strings.ToLower(strings.TrimSpace(expectedRepo))
	if normalizedURL == "" || normalizedExpected == "" {
		return false
	}
	return strings.Contains(normalizedURL, "github.com/"+normalizedExpected+".git") ||
		strings.Contains(normalizedURL, "github.com/"+normalizedExpected)
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
