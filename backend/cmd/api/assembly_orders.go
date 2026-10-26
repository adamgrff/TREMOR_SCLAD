package main

import (
	"encoding/json"
	"github.com/adamgrff/TREMOR_SCLAD/backend/internal/assemblyorders"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
)

func assemblyOrdersHandler(db *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	tx, err := db.Begin(r.Context())
	if err != nil {
		assemblyDraftFailure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `INSERT INTO assembly_preview_drafts(id) VALUES(true) ON CONFLICT DO NOTHING`); err != nil {
		assemblyDraftFailure(w, err)
		return
	}
	var lock int64
	if err = tx.QueryRow(r.Context(), `SELECT version FROM assembly_preview_drafts WHERE id=true FOR UPDATE`).Scan(&lock); err != nil {
		assemblyDraftFailure(w, err)
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT assembly_load_order(h.version_id) FROM assembly_order_heads h JOIN assembly_order_records o ON o.id=h.order_id WHERE o.source='ozon' AND o.status='queued' ORDER BY (SELECT (metadata->>'createdAt')::timestamptz FROM assembly_order_versions WHERE id=h.version_id),o.order_number`)
	if err != nil {
		assemblyDraftFailure(w, err)
		return
	}
	raws := []json.RawMessage{}
	for rows.Next() {
		var raw json.RawMessage
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			assemblyDraftFailure(w, err)
			return
		}
		raws = append(raws, raw)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		assemblyDraftFailure(w, err)
		return
	}
	orders := []any{}
	for _, raw := range raws {
		order := valueMapFromRaw(raw)
		result, e := readAssemblyStock(r.Context(), tx, order, []any{})
		if e != nil {
			assemblyDraftFailure(w, e)
			return
		}
		updateWarehousePlan(order, result, []any{})
		order["stockReady"] = result.Enough
		order["stockMessage"] = stockResultMessage(result)
		orders = append(orders, order)
	}
	// GET does not publish new versions or reserve anything; observations are transient.
	_ = json.NewEncoder(w).Encode(map[string]any{"orders": orders})
}
func ozonConnectionHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// Having credentials is not evidence of a successful connection.
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "disconnected", "configured": assemblyorders.LoadOzonConfig().Configured(), "reason": "Официальный клиент Ozon ещё не подключён."})
}
