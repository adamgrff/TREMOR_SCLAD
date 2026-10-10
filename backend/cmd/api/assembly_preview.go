package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type assemblyDraftResponse struct {
	Version int64           `json:"version"`
	State   json.RawMessage `json:"state"`
}
type assemblyDraftRequest struct {
	RequestID string          `json:"requestId"`
	Version   int64           `json:"version"`
	State     json.RawMessage `json:"state"`
}

var assemblyDraftRequestID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func assemblyDraftError(w http.ResponseWriter, status int, code, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "error": message})
}
func assemblyDraftFailure(w http.ResponseWriter, err error) {
	log.Printf("assembly preview draft: %v", err)
	assemblyDraftError(w, http.StatusInternalServerError, "assembly_draft_unavailable", "Не удалось сохранить или загрузить сборку. Проверьте подключение к backend.")
}

// Preview state is isolated from inventory. Real picking requires separate transactional APIs.
func validateAssemblyDraft(data json.RawMessage) bool {
	var state struct {
		SchemaVersion int `json:"schemaVersion"`
		ActiveOrder   *struct {
			Number string `json:"number"`
			Items  []struct {
				SKU      string `json:"sku"`
				Quantity int    `json:"quantity"`
			} `json:"items"`
		} `json:"activeOrder"`
		ActiveState struct {
			Picks []struct {
				SKU  string `json:"sku"`
				Cell string `json:"cell"`
			} `json:"picks"`
			ConfirmedCell     *string `json:"confirmedCell"`
			ShipmentConfirmed bool    `json:"shipmentConfirmed"`
		} `json:"activeState"`
		ProblemOrders   []json.RawMessage `json:"problemOrders"`
		CompletedOrders []json.RawMessage `json:"completedOrders"`
	}
	if json.Unmarshal(data, &state) != nil || state.SchemaVersion != 1 || len(state.ActiveState.Picks) > 10000 {
		return false
	}
	if state.ActiveOrder == nil {
		return len(state.ActiveState.Picks) == 0 && state.ActiveState.ConfirmedCell == nil && !state.ActiveState.ShipmentConfirmed
	}
	if state.ActiveOrder.Number == "" || len(state.ActiveOrder.Items) == 0 {
		return false
	}
	needed := make(map[string]int)
	total := 0
	for _, item := range state.ActiveOrder.Items {
		if item.SKU == "" || item.Quantity <= 0 || item.Quantity > 10000 || needed[item.SKU] > 0 {
			return false
		}
		needed[item.SKU] = item.Quantity
		total += item.Quantity
	}
	for _, pick := range state.ActiveState.Picks {
		if pick.Cell == "" || needed[pick.SKU] <= 0 {
			return false
		}
		needed[pick.SKU]--
	}
	return !state.ActiveState.ShipmentConfirmed || (len(state.ActiveState.Picks) == total && state.ActiveState.ConfirmedCell == nil)
}

func getAssemblyDraftHandler(db *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	response := assemblyDraftResponse{State: json.RawMessage("null")}
	err := db.QueryRow(r.Context(), `SELECT version, state FROM assembly_preview_drafts WHERE id = TRUE`).Scan(&response.Version, &response.State)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		assemblyDraftFailure(w, err)
		return
	}
	if response.State == nil {
		response.State = json.RawMessage("null")
	}
	_ = json.NewEncoder(w).Encode(response)
}

func saveAssemblyDraftHandler(db *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request assemblyDraftRequest
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || request.Version < 0 || !assemblyDraftRequestID.MatchString(request.RequestID) || !validateAssemblyDraft(request.State) {
		assemblyDraftError(w, 400, "invalid_assembly_draft", "Некорректное состояние сборки.")
		return
	}
	canonical, _ := json.Marshal(request)
	hash := sha256.Sum256(canonical)
	payloadHash := hex.EncodeToString(hash[:])
	tx, err := db.Begin(r.Context())
	if err != nil {
		assemblyDraftFailure(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `INSERT INTO assembly_preview_drafts(id) VALUES(TRUE) ON CONFLICT DO NOTHING`); err != nil {
		assemblyDraftFailure(w, err)
		return
	}
	var currentVersion int64
	if err = tx.QueryRow(r.Context(), `SELECT version FROM assembly_preview_drafts WHERE id = TRUE FOR UPDATE`).Scan(&currentVersion); err != nil {
		assemblyDraftFailure(w, err)
		return
	}
	var existingHash string
	var existingResponse json.RawMessage
	err = tx.QueryRow(r.Context(), `SELECT payload_hash, response FROM assembly_preview_requests WHERE request_id = $1::uuid`, request.RequestID).Scan(&existingHash, &existingResponse)
	if err == nil {
		if existingHash != payloadHash {
			assemblyDraftError(w, 409, "request_id_conflict", "Повтор запроса содержит другие данные.")
			return
		}
		_, _ = w.Write(existingResponse)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		assemblyDraftFailure(w, err)
		return
	}
	if request.Version != currentVersion {
		assemblyDraftError(w, 409, "assembly_draft_conflict", "Сборка изменена в другом окне. Загрузите актуальное состояние.")
		return
	}
	response := assemblyDraftResponse{Version: currentVersion + 1, State: request.State}
	body, err := json.Marshal(response)
	if err != nil {
		assemblyDraftFailure(w, err)
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE assembly_preview_drafts SET version = $1, state = $2::jsonb, updated_at = NOW() WHERE id = TRUE`, response.Version, []byte(request.State)); err != nil {
		assemblyDraftFailure(w, err)
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO assembly_preview_requests(request_id,payload_hash,response) VALUES($1::uuid,$2,$3::jsonb)`, request.RequestID, payloadHash, body); err != nil {
		assemblyDraftFailure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		assemblyDraftFailure(w, err)
		return
	}
	_, _ = w.Write(body)
}
