package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/freeCodeCamp/artemis/internal/githubapp"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	appID := os.Getenv("GH_APP_ID")
	instID := os.Getenv("GH_APP_INSTALLATION_ID")
	pem := os.Getenv("GH_APP_PRIVATE_KEY")
	org := envOr("GH_ORG", "freeCodeCamp-Universe")
	apiBase := envOr("GH_API_BASE", "https://api.github.com")

	if appID == "" || instID == "" || pem == "" {
		fmt.Fprintln(os.Stderr, "preflight: set GH_APP_ID, GH_APP_INSTALLATION_ID, GH_APP_PRIVATE_KEY")
		os.Exit(2)
	}

	fmt.Printf("app_id=%q (len %d)  installation_id=%q (len %d)  pem_bytes=%d\n",
		appID, len(appID), instID, len(instID), len(pem))

	signer, err := githubapp.NewAppJWTSigner(appID, pem)
	if err != nil {
		fmt.Fprintf(os.Stderr, "preflight FAIL: signer: %v\n", err)
		os.Exit(1)
	}
	client, err := githubapp.NewClient(githubapp.ClientConfig{
		APIBase:        apiBase,
		Org:            org,
		InstallationID: instID,
		Signer:         signer,
		Now:            time.Now,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "preflight FAIL: client: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	templates, err := client.ListTemplates(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "preflight FAIL: App auth -> installation token failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("preflight OK: App auth works (org=%s, %d template repos visible)\n", org, len(templates))
}
