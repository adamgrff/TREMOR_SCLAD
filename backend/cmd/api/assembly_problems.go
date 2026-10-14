package main

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func assemblyProblemsHandler(db *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	var data json.RawMessage
	err := db.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(assembly_load_problem(id) ORDER BY created_at,recorded_at,id),'[]'::jsonb)
  FROM assembly_problem_records WHERE ($1 OR (disposition <> 'returned' AND problem_metadata->>'closedOutcome' IS NULL)) AND ($2 = '' OR order_id IN (SELECT id FROM assembly_order_records WHERE order_number=$2))`, r.URL.Query().Get("includeClosed") == "true", r.URL.Query().Get("order")).Scan(&data)
	if err != nil {
		assemblyDraftFailure(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]json.RawMessage{"problems": data})
}

func assemblyProblemHandler(db *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	id := r.PathValue("problemID")
	if !assemblyDraftRequestID.MatchString(id) {
		assemblyDraftError(w, 400, "invalid_problem_id", "Некорректный ID проблемы.")
		return
	}
	var data json.RawMessage
	err := db.QueryRow(r.Context(), `SELECT assembly_load_problem(id) FROM assembly_problem_records WHERE id=$1::uuid`, id).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		assemblyDraftError(w, 404, "problem_not_found", "Проблема не найдена.")
		return
	}
	if err != nil {
		assemblyDraftFailure(w, err)
		return
	}
	_, _ = w.Write(data)
}
