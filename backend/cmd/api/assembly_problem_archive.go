package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const archiveWhere = `FROM assembly_problem_records p JOIN assembly_order_records o ON o.id=p.order_id
 WHERE (p.disposition='returned' OR p.problem_metadata->>'closedOutcome' IS NOT NULL)
 AND ($1='' OR p.problem_type=$1)
 AND ($2='' OR CASE WHEN p.disposition='returned' THEN 'returned' ELSE p.problem_metadata->>'closedOutcome' END=$2)
 AND ($3='' OR o.source=$3)
 AND ($4='' OR strpos(lower(o.order_number),lower($4))>0 OR EXISTS(SELECT 1 FROM assembly_order_positions pos
 WHERE pos.version_id IN(p.order_version_id,p.creation_order_version_id)
 AND strpos(lower(concat_ws(' ',pos.sku,pos.payload->>'name',pos.payload->>'ozonSku',pos.payload->>'warehouseSku')),lower($4))>0))`

func archivePositive(value string, fallback, max int) (int, bool) {
	if value == "" {
		return fallback, true
	}
	n, err := strconv.ParseInt(value, 10, 32)
	return int(n), err == nil && n > 0 && n <= int64(max)
}

// Read-only bounded summaries; full histories and source snapshots are fetched separately.
func assemblyProblemArchiveHandler(db *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	q := r.URL.Query()
	page, valid := archivePositive(q.Get("page"), 1, 2147483647)
	size, validSize := archivePositive(q.Get("pageSize"), 5, 50)
	kind, outcome, source, search := q.Get("type"), q.Get("outcome"), q.Get("source"), strings.TrimSpace(q.Get("q"))
	validKind := kind == "" || manualProblemLabels[kind] != "" || kind == "cancelled_order" || kind == "ozon_error"
	validOutcome := outcome == "" || outcome == "returned" || outcome == "completed" || outcome == "cancelled" || outcome == "disassembled"
	if !valid || !validSize || !validKind || !validOutcome || (source != "" && source != "ozon" && source != "preview") || len(search) > 800 {
		assemblyDraftError(w, 400, "invalid_archive_filter", "Некорректные параметры просмотра архива.")
		return
	}
	tx, err := db.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		assemblyDraftFailure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	args := []any{kind, outcome, source, search}
	var total int
	if err = tx.QueryRow(r.Context(), `SELECT count(*) `+archiveWhere, args...).Scan(&total); err != nil {
		assemblyDraftFailure(w, err)
		return
	}
	pages := max(1, (total+size-1)/size)
	page = min(page, pages)
	args = append(args, size, (page-1)*size)
	var data json.RawMessage
	sql := `SELECT COALESCE(jsonb_agg(summary ORDER BY sort_time DESC,recorded_at DESC,id DESC),'[]'::jsonb) FROM (
 SELECT p.id,p.recorded_at,COALESCE((SELECT e.occurred_at FROM assembly_problem_events e WHERE e.problem_id=p.id AND
 ((p.disposition='returned' AND e.kind='return') OR (p.problem_metadata->>'closedOutcome'='disassembled' AND e.kind='disassembly_finish')
 OR (p.problem_metadata->>'closedOutcome'='completed' AND e.kind='ozon_result' AND e.payload->>'title'='Заказ завершён')
 OR (p.problem_metadata->>'closedOutcome'='cancelled' AND e.kind='ozon_cancel' AND e.payload->>'title' IN ('Отменённый заказ закрыт','Заказ закрыт по подтверждённой отмене')))
 ORDER BY e.ordinal DESC LIMIT 1),p.created_at) AS sort_time,
 jsonb_build_object('problemId',p.id,'number',o.order_number,'source',o.source,'orderStatus',o.status,'type',p.problem_type,
 'outcome',CASE WHEN p.disposition='returned' THEN 'returned' ELSE p.problem_metadata->>'closedOutcome' END,'createdAt',p.created_at,
 'closedAt',(SELECT e.occurred_at FROM assembly_problem_events e WHERE e.problem_id=p.id AND
 ((p.disposition='returned' AND e.kind='return') OR (p.problem_metadata->>'closedOutcome'='disassembled' AND e.kind='disassembly_finish')
 OR (p.problem_metadata->>'closedOutcome'='completed' AND e.kind='ozon_result' AND e.payload->>'title'='Заказ завершён')
 OR (p.problem_metadata->>'closedOutcome'='cancelled' AND e.kind='ozon_cancel' AND e.payload->>'title' IN ('Отменённый заказ закрыт','Заказ закрыт по подтверждённой отмене')))
 ORDER BY e.ordinal DESC LIMIT 1),
 'eventCount',(SELECT count(*) FROM assembly_problem_events e WHERE e.problem_id=p.id),
 'positions',COALESCE((SELECT jsonb_agg(jsonb_build_object('sku',a.sku,'name',a.creation_payload->>'name') ORDER BY a.sku) FROM assembly_problem_affected_positions a WHERE a.problem_id=p.id),'[]'::jsonb)) AS summary
 ` + archiveWhere + ` ORDER BY sort_time DESC,p.recorded_at DESC,p.id DESC LIMIT $5 OFFSET $6) selected`
	if err = tx.QueryRow(r.Context(), sql, args...).Scan(&data); err != nil {
		assemblyDraftFailure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		assemblyDraftFailure(w, err)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"problems": data, "total": total, "page": page, "pageSize": size, "pageCount": pages})
}

// Enrichment is outside problem.history; no read can change immutable event payloads.
func assemblyProblemHistoryHandler(db *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	id := r.PathValue("problemID")
	if !assemblyDraftRequestID.MatchString(id) {
		assemblyDraftError(w, 400, "invalid_problem_id", "Некорректный ID проблемы.")
		return
	}
	tx, err := db.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		assemblyDraftFailure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var entry, originalOrder, originalProblem, records, related json.RawMessage
	var oid string
	err = tx.QueryRow(r.Context(), `SELECT assembly_load_problem(id),assembly_load_order(creation_order_version_id),creation_problem_metadata,order_id::text FROM assembly_problem_records WHERE id=$1::uuid`, id).Scan(&entry, &originalOrder, &originalProblem, &oid)
	if errors.Is(err, pgx.ErrNoRows) {
		assemblyDraftError(w, 404, "problem_not_found", "Проблема не найдена.")
		return
	}
	if err != nil {
		assemblyDraftFailure(w, err)
		return
	}
	if err = tx.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(payload||jsonb_build_object('id',id,'ordinal',ordinal,'occurredAt',occurred_at,'recordedAt',recorded_at) ORDER BY ordinal),'[]'::jsonb) FROM assembly_problem_events WHERE problem_id=$1::uuid`, id).Scan(&records); err != nil {
		assemblyDraftFailure(w, err)
		return
	}
	if err = tx.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(jsonb_build_object('problemId',id,'type',problem_type,'resolutionStatus',resolution_status,'disposition',disposition,'closedOutcome',problem_metadata->>'closedOutcome','createdAt',created_at) ORDER BY created_at,recorded_at,id),'[]'::jsonb) FROM assembly_problem_records WHERE order_id=$1::uuid`, oid).Scan(&related); err != nil {
		assemblyDraftFailure(w, err)
		return
	}
	journals := map[string]json.RawMessage{}
	var enabled bool
	if err = tx.QueryRow(r.Context(), `SELECT to_regclass('assembly_stock_checks') IS NOT NULL`).Scan(&enabled); err != nil {
		assemblyDraftFailure(w, err)
		return
	}
	if enabled {
		var checks json.RawMessage
		if err = tx.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(jsonb_build_object('id',c.id,'recordedAt',c.recorded_at,'purpose',c.purpose,'result',c.result) ORDER BY c.recorded_at,c.id),'[]'::jsonb) FROM assembly_stock_checks c WHERE c.order_id=$2::uuid AND EXISTS(SELECT 1 FROM assembly_problem_events e WHERE e.problem_id=$1::uuid AND e.payload->>'checkId'=c.id::text)`, id, oid).Scan(&checks); err != nil {
			assemblyDraftFailure(w, err)
			return
		}
		journals["stockChecks"] = checks
	}
	if err = tx.QueryRow(r.Context(), `SELECT to_regclass('assembly_ozon_problem_links') IS NOT NULL`).Scan(&enabled); err != nil {
		assemblyDraftFailure(w, err)
		return
	}
	if enabled {
		var ozon json.RawMessage
		if err = tx.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(jsonb_build_object('id',op.id,'purpose',op.purpose,'createdAt',op.created_at,
 'attempts',COALESCE((SELECT jsonb_agg(jsonb_build_object('requestId',r.request_id,'kind',r.kind,'recordedAt',r.recorded_at) ORDER BY r.recorded_at,r.request_id) FROM assembly_ozon_reconciliations r WHERE r.operation_id=op.id),'[]'::jsonb),
 'observations',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',obs.id,'evidenceId',obs.evidence_id,'outcome',obs.outcome,'observedAt',obs.observed_at,'recordedAt',obs.recorded_at) ORDER BY obs.observed_at,obs.recorded_at,obs.id) FROM assembly_ozon_observations obs WHERE obs.operation_id=op.id),'[]'::jsonb)) ORDER BY op.created_at,op.id),'[]'::jsonb) FROM assembly_ozon_operations op JOIN assembly_ozon_problem_links l ON l.operation_id=op.id WHERE l.problem_id=$1::uuid`, id).Scan(&ozon); err != nil {
			assemblyDraftFailure(w, err)
			return
		}
		journals["ozon"] = ozon
	}
	if err = tx.QueryRow(r.Context(), `SELECT to_regclass('assembly_disassembly_sessions') IS NOT NULL`).Scan(&enabled); err != nil {
		assemblyDraftFailure(w, err)
		return
	}
	if enabled {
		var disassembly json.RawMessage
		if err = tx.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(jsonb_build_object('id',s.id,'status',s.status,'createdAt',s.created_at,'completedAt',s.completed_at,
 'returns',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',r.id,'operationId',i.original_operation_id,'sku',i.payload->>'sku','cellId',r.cell_id,'cell',i.payload->>'sourceCell','quantity',r.quantity,'occurredAt',r.occurred_at) ORDER BY r.occurred_at,r.id) FROM assembly_disassembly_items i JOIN assembly_disassembly_returns r ON r.item_id=i.id WHERE i.session_id=s.id),'[]'::jsonb)) ORDER BY s.created_at,s.id),'[]'::jsonb) FROM assembly_disassembly_sessions s WHERE s.order_id=$1::uuid`, oid).Scan(&disassembly); err != nil {
			assemblyDraftFailure(w, err)
			return
		}
		journals["disassembly"] = disassembly
	}
	if err = tx.Commit(r.Context()); err != nil {
		assemblyDraftFailure(w, err)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"entry": entry, "originalOrder": originalOrder, "originalProblem": originalProblem, "historyRecords": records, "relatedProblems": related, "journals": journals})
}
