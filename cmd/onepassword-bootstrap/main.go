// runx-public-repo-gate: allow-file *
// Command onepassword-bootstrap creates the 1Password Login items for the
// Helixon fleet, using the official 1Password Go SDK with a service account
// token. This is the fallback path for environments where `op` CLI write
// commands hang (the SDK does not require the 1Password desktop app).
//
// Usage:
//
//	export OP_SERVICE_ACCOUNT_TOKEN=$(cat ~/.config/op/service-account-token)
//	./onepassword-bootstrap --vault HelixonSafe
//
// Items created (idempotent: skips if title already exists):
//   - jason@win2   (Login: Win PC WSL Ubuntu Login GB password)
//   - jason@win4   (Login: Win PC WSL Ubuntu Login GB password)
//   - HF_TOKEN     (api_credential: HuggingFace token from env HF_TOKEN)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/1password/onepassword-sdk-go"
)

const integrationName = "helixon-platform-onepassword-bootstrap"
const integrationVersion = "0.1.0"

type itemSpec struct {
	Title    string
	Category onepassword.ItemCategory
	Fields   []onepassword.ItemField
}

func main() {
	vaultName := flag.String("vault", "HelixonSafe", "1Password vault name (must be accessible by the service account)")
	password := flag.String("password", os.Getenv("HELIXON_UNIVERSAL_PASSWORD"), "Universal Windows password to store in the Login items")
	hfToken := flag.String("hf-token", os.Getenv("HF_TOKEN"), "HuggingFace token to store in the api_credential item")
	timeout := flag.Duration("timeout", 30*time.Second, "SDK call timeout")
	flag.Parse()

	token := os.Getenv("OP_SERVICE_ACCOUNT_TOKEN")
	if token == "" {
		log.Fatal("OP_SERVICE_ACCOUNT_TOKEN is required (export from ~/.config/op/service-account-token)")
	}
	if *password == "" {
		log.Fatal("--password (or HELIXON_UNIVERSAL_PASSWORD env) is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	client, err := onepassword.NewClient(ctx,
		onepassword.WithServiceAccountToken(token),
		onepassword.WithIntegrationInfo(integrationName, integrationVersion),
	)
	if err != nil {
		//nolint:gocritic // exitAfterDefer: cancel is on parent context which is process-scoped; Fatalf is correct.
		log.Fatalf("onepassword.NewClient: %v", err)
	}

	vaultID, err := resolveVaultID(ctx, client, *vaultName)
	if err != nil {
		log.Fatalf("resolveVaultID(%q): %v", *vaultName, err)
	}
	log.Printf("vault: name=%s id=%s", *vaultName, vaultID)

	// Build the list of items to upsert.
	items := []itemSpec{
		{
			Title:    "Win PC WSL Ubuntu Login GB / jason@win2",
			Category: onepassword.ItemCategoryLogin,
			Fields: []onepassword.ItemField{
				{ID: "username", Title: "username", Value: "jason", FieldType: onepassword.ItemFieldTypeText},
				{ID: "password", Title: "password", Value: *password, FieldType: onepassword.ItemFieldTypeConcealed},
				{ID: "host", Title: "host", Value: "win2", FieldType: onepassword.ItemFieldTypeText},
				{ID: "notes", Title: "notes", Value: "Auto-created by helixon-platform v14508.5 bootstrap. Universal Windows password; same value as 'Win PC WSL Ubuntu Login GB' Login item.", FieldType: onepassword.ItemFieldTypeText},
			},
		},
		{
			Title:    "Win PC WSL Ubuntu Login GB / jason@win4",
			Category: onepassword.ItemCategoryLogin,
			Fields: []onepassword.ItemField{
				{ID: "username", Title: "username", Value: "jason", FieldType: onepassword.ItemFieldTypeText},
				{ID: "password", Title: "password", Value: *password, FieldType: onepassword.ItemFieldTypeConcealed},
				{ID: "host", Title: "host", Value: "win4", FieldType: onepassword.ItemFieldTypeText},
				{ID: "notes", Title: "notes", Value: "Auto-created by helixon-platform v14508.5 bootstrap. Universal Windows password; same value as 'Win PC WSL Ubuntu Login GB' Login item.", FieldType: onepassword.ItemFieldTypeText},
			},
		},
	}
	if *hfToken != "" {
		items = append(items, itemSpec{
			Title:    "HF_TOKEN",
			Category: onepassword.ItemCategoryAPICredentials,
			Fields: []onepassword.ItemField{
				{ID: "credential", Title: "credential", Value: *hfToken, FieldType: onepassword.ItemFieldTypeConcealed},
				{ID: "notes", Title: "notes", Value: "Auto-created by helixon-platform v14508.5 bootstrap. Injected via `op read op://HelixonSafe/HF_TOKEN/credential` (CLI) or one.password-sdk-go Secrets().Resolve (programmatic).", FieldType: onepassword.ItemFieldTypeText},
			},
		})
	}

	for _, spec := range items {
		if err := upsertItem(ctx, client, vaultID, spec); err != nil {
			log.Fatalf("upsertItem(%q): %v", spec.Title, err)
		}
	}

	log.Printf("done: %d item(s) processed", len(items))
}

func resolveVaultID(ctx context.Context, client *onepassword.Client, name string) (string, error) {
	vaults, err := client.Vaults().List(ctx)
	if err != nil {
		return "", fmt.Errorf("Vaults().List: %w", err)
	}
	for _, v := range vaults {
		if v.Title == name {
			return v.ID, nil
		}
	}
	return "", fmt.Errorf("vault %q not found (available: %d)", name, len(vaults))
}

func upsertItem(ctx context.Context, client *onepassword.Client, vaultID string, spec itemSpec) error {
	// Idempotency: list items in the vault and skip if title already exists.
	existing, err := client.Items().List(ctx, vaultID)
	if err != nil {
		return fmt.Errorf("Items().List: %w", err)
	}
	for _, item := range existing {
		if item.Title == spec.Title {
			log.Printf("skip: %q already exists (id=%s)", spec.Title, item.ID)
			return nil
		}
	}

	params := onepassword.ItemCreateParams{
		Title:    spec.Title,
		Category: spec.Category,
		VaultID:  vaultID,
		Fields:   spec.Fields,
		Tags:     []string{"helixon-bootstrap", "v14508.5"},
	}
	created, err := client.Items().Create(ctx, params)
	if err != nil {
		return fmt.Errorf("Items().Create: %w", err)
	}
	if created.ID == "" {
		return errors.New("Items().Create returned empty ID")
	}
	log.Printf("created: %q (id=%s)", created.Title, created.ID)
	return nil
}
