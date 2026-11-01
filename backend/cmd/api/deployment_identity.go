package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const deploymentHeader = "X-Tremor-Deployment-Id"

// Reads identity in the same snapshot as UI data. Legacy schemas remain
// viewable, but an empty identity never authorizes a frontend mutation.
func receivingSnapshotDeployment(ctx context.Context, tx pgx.Tx) (string, error) {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('deployment_identity') IS NOT NULL`).Scan(&exists); err != nil {
		return "", err
	}
	if !exists {
		return "", nil
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT deployment_id::text FROM deployment_identity WHERE singleton`).Scan(&id)
	return id, err
}

func deploymentError(w http.ResponseWriter, status int, code string) {
	w.Header().Del("X-Receiving-Command-Rejected")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "error": "Deployment identity is missing, unavailable or different; preserve the command and verify the deployment"})
}

// Must be the first operation-specific query, including before receipt replay.
// The row lock is retained until COMMIT/rollback and serializes an explicit
// offline re-identification with in-flight receiving transactions.
func requireReceivingDeployment(w http.ResponseWriter, r *http.Request, tx pgx.Tx) bool {
	expected := strings.ToLower(r.Header.Get(deploymentHeader))
	if !receivingScanRequestID.MatchString(expected) {
		deploymentError(w, 400, "deployment_identity_required")
		return false
	}
	var actual string
	if err := tx.QueryRow(r.Context(), `SELECT deployment_id::text FROM deployment_identity WHERE singleton FOR SHARE`).Scan(&actual); err != nil {
		deploymentError(w, 503, "deployment_identity_unavailable")
		return false
	}
	if actual != expected {
		deploymentError(w, 409, "deployment_identity_mismatch")
		return false
	}
	return true
}

func deploymentIdentityHandler(db *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	tx, err := db.BeginTx(r.Context(), pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		deploymentError(w, 503, "deployment_identity_unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	var id string
	if err = tx.QueryRow(r.Context(), `SELECT deployment_id::text FROM deployment_identity WHERE singleton`).Scan(&id); err != nil || !receivingScanRequestID.MatchString(id) {
		deploymentError(w, 503, "deployment_identity_unavailable")
		return
	}
	if tx.Commit(r.Context()) != nil {
		deploymentError(w, 503, "deployment_identity_unavailable")
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"deploymentId": id})
}
