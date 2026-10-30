package main

import (
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
)

// Reject production targets before a test helper can create or drop schemas.
func requireSafeReceivingTestDatabase(t *testing.T, testURL string) {
	t.Helper()
	test, err := pgx.ParseConfig(testURL)
	if err != nil {
		t.Fatal("invalid TEST_DATABASE_URL")
	}
	if strings.EqualFold(test.Database, "tremor_sklad") {
		t.Fatal("TEST_DATABASE_URL must not target tremor_sklad")
	}
	productionURL := os.Getenv("DATABASE_URL")
	if productionURL == "" {
		values, readErr := godotenv.Read("../../.env")
		if readErr != nil && !os.IsNotExist(readErr) {
			t.Fatal("cannot safely read backend database configuration")
		}
		productionURL = values["DATABASE_URL"]
	}
	if productionURL != "" {
		production, parseErr := pgx.ParseConfig(productionURL)
		if parseErr != nil {
			t.Fatal("cannot safely identify DATABASE_URL target")
		}
		// Deliberately conservative: equal database names are rejected even on
		// different hosts, avoiding aliases/local tunnel ambiguity.
		if strings.EqualFold(test.Database, production.Database) {
			t.Fatal("TEST_DATABASE_URL must name a different database from DATABASE_URL")
		}
	}
}
