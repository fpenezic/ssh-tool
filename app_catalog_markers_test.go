package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"ssh-tool/internal/store"
)

// The app-level consumers of a materialised catalog reference: the first
// hop's network profile, the database login and the Kafka SASL login.
// Each must stop on a marker with the typed error, never fall back to a
// direct route or no login.
func TestCatalogMarkersStopAppPaths(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &App{db: db}
	unbound := store.CatalogUnboundRef("cat", "slot")
	missing := store.CatalogMissingRef("cat", "rec")

	for _, ref := range []string{unbound, missing} {
		var re *store.CatalogRefError
		if _, err := a.wgDialerForCtx(context.Background(), ref); !errors.As(err, &re) || re.What != "network profile" {
			t.Errorf("network profile %s: %v", ref, err)
		}
		r := ref
		if _, _, err := a.dbLogin(&store.ResolvedSettings{DbCredentialRef: &r}); !errors.As(err, &re) || re.What != "credential" {
			t.Errorf("database login %s: %v", ref, err)
		}
		if _, err := a.kafkaSASL(&store.ResolvedSettings{KafkaSASL: "plain", KafkaCredentialRef: &r}); !errors.As(err, &re) || re.What != "credential" {
			t.Errorf("kafka login %s: %v", ref, err)
		}
	}
}
