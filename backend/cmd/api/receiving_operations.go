package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type receivingCommandRequest struct {
	RequestID string `json:"requestId"`
}

func decodeReceivingCommand(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(new(any)) != io.EOF {
		writeErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

func lockReceivingRequest(ctx context.Context, tx pgx.Tx, token string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, strings.ToLower(token))
	return err
}

type receivingOperation struct {
	pgx.Tx
	requestID string
	operation string
	payload   []byte
}

// All receiving commands acquire the UUID lock before the session/stock locks.
// A nil operation means a replay or error has already been written to HTTP.
func beginReceivingOperation(db *pgxpool.Pool, w http.ResponseWriter, r *http.Request, token, operation string, payload any) *receivingOperation {
	w.Header().Set("Access-Control-Expose-Headers", "X-Receiving-Command-Rejected")
	token = strings.ToLower(strings.TrimSpace(token))
	if !receivingScanRequestID.MatchString(token) {
		writeErrorResponse(w, http.StatusBadRequest, "requestId must be a UUID")
		return nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		writeErrorResponse(w, 500, "invalid command payload")
		return nil
	}
	// Canonical payload excludes delivery identity; operation is compared separately.
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		writeErrorResponse(w, 500, "invalid command payload")
		return nil
	}
	delete(fields, "requestId")
	raw, _ = json.Marshal(fields)
	tx, err := db.Begin(r.Context())
	if err != nil {
		writeErrorResponse(w, 500, "failed to start receiving command")
		return nil
	}
	fail := func(status int, message string) *receivingOperation {
		_ = tx.Rollback(r.Context())
		writeErrorResponse(w, status, message)
		return nil
	}
	if err = lockReceivingRequest(r.Context(), tx, token); err != nil {
		return fail(500, "failed to lock receiving command")
	}
	var same bool
	var status int
	var response json.RawMessage
	err = tx.QueryRow(r.Context(), `SELECT operation=$2 AND payload=$3::jsonb,status_code,response FROM receiving_operation_requests WHERE request_id=$1::uuid`, token, operation, raw).Scan(&same, &status, &response)
	if err == nil {
		_ = tx.Rollback(r.Context())
		if !same {
			writeErrorResponse(w, 409, "requestId already used for another receiving command")
			return nil
		}
		w.WriteHeader(status)
		_, _ = w.Write(response)
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fail(500, "failed to read receiving receipt")
	}
	var legacy bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM receiving_quantity_requests WHERE lower(request_id)=$1)`, token).Scan(&legacy); err != nil {
		return fail(500, "failed to read receiving receipt")
	}
	if legacy {
		return fail(409, "requestId already used for another receiving command")
	}
	// Only explicit pre-COMMIT business rejections carry this marker. A UUID
	// conflict or an uncertain COMMIT must never be discarded by the client.
	w.Header().Set("X-Receiving-Command-Rejected", "true")
	return &receivingOperation{Tx: tx, requestID: token, operation: operation, payload: raw}
}

func (op *receivingOperation) respond(w http.ResponseWriter, r *http.Request, status int, result any) {
	raw, err := json.Marshal(result)
	if err == nil {
		err = op.QueryRow(r.Context(), `INSERT INTO receiving_operation_requests(request_id,operation,payload,response,status_code) VALUES($1::uuid,$2,$3::jsonb,$4::jsonb,$5) RETURNING response`, op.requestID, op.operation, op.payload, raw, status).Scan(&raw)
	}
	if err != nil {
		writeErrorResponse(w, 500, "failed to save receiving receipt")
		return
	}
	w.Header().Del("X-Receiving-Command-Rejected")
	if err = op.Commit(r.Context()); err != nil {
		writeErrorResponse(w, 500, "receiving command commit outcome is unknown; retry the same requestId")
		return
	}
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
