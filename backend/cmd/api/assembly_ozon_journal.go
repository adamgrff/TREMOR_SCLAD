package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/adamgrff/TREMOR_SCLAD/backend/internal/assemblyorders"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This is an internal domain record, NOT an Ozon transport contract or an HTTP input.
// Only a future client verified against the official specification may supply receipts.
// There is deliberately no dispatch method: reconciliation never sends a business operation.
type ozonDomainReceipt struct {
	OperationID string
	EvidenceID  string
	OrderNumber string
	PayloadHash string
	Outcome     string // unknown, not_applied, applied, rejected, cancelled
	ObservedAt  string
	Order       json.RawMessage // mandatory for a verified refresh, using the existing input model
}
type ozonJournalOperation struct {
	ID, OrderID, VersionID, Purpose, BusinessKey, Hash, Outcome string
	Payload                                                     json.RawMessage
}

func loadOzonOperation(ctx context.Context, tx pgx.Tx, id string) (ozonJournalOperation, error) {
	op := ozonJournalOperation{}
	err := tx.QueryRow(ctx, `SELECT id::text,order_id::text,order_version_id::text,purpose,business_key::text,payload_hash,payload,COALESCE((SELECT outcome FROM assembly_ozon_observations WHERE operation_id=o.id ORDER BY observed_at DESC,recorded_at DESC,id DESC LIMIT 1),'unknown') FROM assembly_ozon_operations o WHERE id=$1::uuid FOR UPDATE`, id).Scan(&op.ID, &op.OrderID, &op.VersionID, &op.Purpose, &op.BusinessKey, &op.Hash, &op.Payload, &op.Outcome)
	return op, err
}

func registerOzonOperation(ctx context.Context, tx pgx.Tx, order, active map[string]any, purpose string) (ozonJournalOperation, error) {
	var vid, oid string
	if order["source"] != "ozon" {
		return ozonJournalOperation{}, stockConflict("Внешние операции разрешены только для реального заказа.")
	}
	if err := tx.QueryRow(ctx, `SELECT assembly_store_order($1::jsonb)::text`, []byte(encodeValue(order))).Scan(&vid); err != nil {
		return ozonJournalOperation{}, err
	}
	if err := tx.QueryRow(ctx, `SELECT order_id::text FROM assembly_order_versions WHERE id=$1::uuid`, vid).Scan(&oid); err != nil {
		return ozonJournalOperation{}, err
	}
	items := []any{}
	for _, v := range valueArray(order["items"]) {
		item := valueMap(v)
		items = append(items, map[string]any{"sku": item["sku"], "ozonSku": item["ozonSku"], "quantity": item["quantity"], "productId": item["productId"]})
	}
	semantic := encodeValue(map[string]any{"number": order["number"], "purpose": purpose, "items": items, "picks": active["picks"], "shipmentConfirmed": active["shipmentConfirmed"]})
	digest := sha256.Sum256(semantic)
	hash := hex.EncodeToString(digest[:])
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM assembly_ozon_operations WHERE order_id=$1::uuid AND purpose=$2 AND payload_hash=$3`, oid, purpose, hash).Scan(&id)
	if err == nil {
		return loadOzonOperation(ctx, tx, id)
	}
	if err != pgx.ErrNoRows {
		return ozonJournalOperation{}, err
	}
	var unresolved bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assembly_ozon_operations o WHERE order_id=$1::uuid AND purpose=$2 AND COALESCE((SELECT outcome FROM assembly_ozon_observations WHERE operation_id=o.id ORDER BY observed_at DESC,recorded_at DESC,id DESC LIMIT 1),'unknown') NOT IN ('rejected','not_applied'))`, oid, purpose).Scan(&unresolved); err != nil {
		return ozonJournalOperation{}, err
	}
	if unresolved {
		return ozonJournalOperation{}, stockConflict("Исходная операция Ozon имеет неизвестный или уже подтверждённый результат. Новая бизнес-операция запрещена.")
	}
	var origin string
	if err = tx.QueryRow(ctx, `SELECT CASE WHEN status='in_progress' THEN 'assembly' ELSE 'queue' END FROM assembly_order_records WHERE id=$1::uuid`, oid).Scan(&origin); err != nil {
		return ozonJournalOperation{}, err
	}
	payload := encodeValue(map[string]any{"order": order, "assembly": active, "source": origin})
	if err = tx.QueryRow(ctx, `INSERT INTO assembly_ozon_operations(order_id,order_version_id,purpose,payload,payload_hash) VALUES($1::uuid,$2::uuid,$3,$4::jsonb,$5) RETURNING id::text`, oid, vid, purpose, []byte(payload), hash).Scan(&id); err != nil {
		return ozonJournalOperation{}, err
	}
	return loadOzonOperation(ctx, tx, id)
}

func appendOzonCause(ctx context.Context, tx pgx.Tx, state map[string]any, order, snapshot map[string]any, reason, comment, timestamp string) (map[string]any, map[string]any, error) {
	originalActive, originalState := state["activeOrder"], state["activeState"]
	base, _ := decodeWorkspace(encodeValue(state))
	request := assemblyProblemCreationRequest{Source: "queue", OrderNumber: textValue(order["number"]), Order: encodeValue(order), AllowExistingQueue: true, Details: assemblyProblemDetails{Type: reason, Comment: comment}}
	if reason == "insufficient_stock" || reason == "unknown_sku" {
		for _, v := range valueArray(order["items"]) {
			item := valueMap(v)
			taken := 0
			for _, pick := range valueArray(snapshot["picks"]) {
				if valueMap(pick)["sku"] == item["sku"] {
					taken++
				}
			}
			if (reason == "insufficient_stock" && intValue(item["productId"]) != 0 && intValue(item["stock"]) < intValue(item["quantity"])-taken) || (reason == "unknown_sku" && intValue(item["productId"]) == 0) {
				request.Details.PositionSKU = textValue(item["sku"])
				break
			}
		}
	}
	if snapshot != nil {
		base["activeOrder"] = order
		base["activeState"] = snapshot
		request.Source = "assembly"
		request.Order = nil
	}
	raw, err := buildAssemblyProblem(encodeValue(base), request, timestamp)
	if err != nil {
		return nil, nil, err
	}
	// Ozon-only types cannot be created by the public manual problem endpoint.
	next, _ := decodeWorkspace(raw)
	entries := valueArray(next["problemOrders"])
	entry := valueMap(entries[len(entries)-1])
	p := valueMap(entry["problem"])
	if reason == "ozon_error" || reason == "cancelled_order" {
		events := valueArray(p["history"])
		label := map[string]string{"ozon_error": "Подтверждение Ozon не получено", "cancelled_order": "Отмена заказа подтверждена Ozon"}[reason]
		for _, e := range events {
			if valueMap(e)["kind"] == "transfer" {
				valueMap(e)["message"] = label
			}
		}
	}
	raw, err = enrichWarehouseProblemHistory(ctx, tx, encodeValue(next))
	if err != nil {
		return nil, nil, err
	}
	next, _ = decodeWorkspace(raw)
	entries = valueArray(next["problemOrders"])
	entry = valueMap(entries[len(entries)-1])
	// Temporary hydration of a stopped order must not stop a different active order.
	next["activeOrder"], next["activeState"] = originalActive, originalState
	if sameAssemblyOrder(valueMap(originalActive), order) {
		next["activeOrder"] = nil
		next["activeState"] = previousEmptyState()
	}
	return next, entry, nil
}

func prepareOzonConfirmation(ctx context.Context, tx pgx.Tx, state, order, active map[string]any, request assemblyProblemOperation, timestamp string) (json.RawMessage, string, bool, error) {
	op, err := registerOzonOperation(ctx, tx, order, active, "confirm_shipment")
	if err != nil {
		return nil, "", false, err
	}
	message := "Отправка в Ozon отключена до официального контракта. Исходная операция сохранена; подтверждение не получено. Повтор выполняет только сверку результата."
	next, entry, err := appendOzonCause(ctx, tx, state, order, active, "ozon_error", message, timestamp)
	if err != nil {
		return nil, "", false, err
	}
	problem := valueMap(entry["problem"])
	problem["ozonOperationId"], problem["ozonPurpose"], problem["ozonOutcome"] = op.ID, op.Purpose, "unknown"
	valueMap(problem["creationContext"])["ozonOperationId"] = op.ID
	appendProblemEvent(problem, timestamp, "ozon_reconcile", "Подтверждение Ozon ожидает сверки", message, "neutral", "")
	if _, err = tx.Exec(ctx, `INSERT INTO assembly_ozon_reconciliations(request_id,operation_id,kind) VALUES($1::uuid,$2::uuid,'unavailable')`, request.RequestID, op.ID); err != nil {
		return nil, "", false, err
	}
	return encodeValue(next), message, false, nil
}

func applyOzonProblemAction(ctx context.Context, tx pgx.Tx, current json.RawMessage, request assemblyProblemOperation, timestamp string) (json.RawMessage, string, bool, error) {
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('assembly_ozon_operations') IS NOT NULL`).Scan(&enabled); err != nil {
		return nil, "", false, err
	}
	if !enabled {
		return nil, "", false, stockConflict("Примените миграцию 017.")
	}
	state, err := decodeWorkspace(current)
	if err != nil {
		return nil, "", false, err
	}
	var selected map[string]any
	for _, v := range valueArray(state["problemOrders"]) {
		e := valueMap(v)
		if e["problemId"] == request.ProblemID {
			selected = e
			break
		}
	}
	if selected == nil {
		return nil, "", false, stockConflict("Проблема не найдена.")
	}
	p := valueMap(selected["problem"])
	order := valueMap(selected["order"])
	if order["source"] != "ozon" || p["closedOutcome"] != nil || p["resolutionStatus"] == "returned" {
		return nil, "", false, stockConflict("Проблема уже закрыта или относится к историческому примеру.")
	}
	if p["type"] != "ozon_error" && !(request.Action == "close-cancelled" && p["type"] == "cancelled_order") {
		return nil, "", false, stockConflict("Действие не относится к этой причине.")
	}
	id := textValue(p["ozonOperationId"])
	if !assemblyDraftRequestID.MatchString(id) {
		return nil, "", false, stockConflict("У проблемы нет исходной серверной операции Ozon. Создание новой операции кнопкой повтора запрещено.")
	}
	op, err := loadOzonOperation(ctx, tx, id)
	if err != nil {
		return nil, "", false, err
	}
	saved := valueMapFromRaw(op.Payload)
	if !sameAssemblyOrder(valueMap(saved["order"]), order) {
		return nil, "", false, stockConflict("Операция относится к другому заказу.")
	}
	if request.Action == "close-cancelled" {
		if op.Outcome != "cancelled" {
			return nil, "", false, stockConflict("Нет проверенного подтверждения отмены.")
		}
		picks := valueArray(valueMap(selected["assembly"])["picks"])
		if err = verifyWarehousePicks(ctx, tx, op.OrderID, picks); err != nil {
			return nil, "", false, err
		}
		var remaining int
		if err = tx.QueryRow(ctx, `SELECT COALESCE(sum(quantity),0)::int FROM assembly_order_reservations WHERE order_id=$1::uuid AND status='held'`, op.OrderID).Scan(&remaining); err != nil {
			return nil, "", false, err
		}
		if len(picks) > 0 || remaining > 0 || p["requiresDisassembly"] == true {
			return nil, "", false, stockConflict("За заказом остаются отборы, резерв или обязательный разбор. Закрытие запрещено.")
		}
		for _, v := range valueArray(state["problemOrders"]) {
			e := valueMap(v)
			other := valueMap(e["problem"])
			if sameAssemblyOrder(valueMap(e["order"]), order) && other["resolutionStatus"] != "returned" && other["closedOutcome"] == nil {
				other["closedOutcome"] = "cancelled"
				appendProblemEvent(other, timestamp, "ozon_cancel", "Заказ закрыт по подтверждённой отмене", "Причина сохранена в истории; заказ не возвращается в сборку.", "neutral", "")
			}
		}
		p["resolutionStatus"], p["closedOutcome"] = "resolved", "cancelled"
		appendProblemEvent(p, timestamp, "ozon_cancel", "Отменённый заказ закрыт", "Проверенная отмена, отсутствие отборов и оставшегося резерва подтверждены.", "success", "")
		return encodeValue(state), "Отменённый заказ закрыт. История сохранена.", true, nil
	}
	// Every click refers to the original business key. There is no external dispatch path here.
	if _, err = tx.Exec(ctx, `INSERT INTO assembly_ozon_reconciliations(request_id,operation_id,kind) VALUES($1::uuid,$2::uuid,'unavailable')`, request.RequestID, id); err != nil {
		return nil, "", false, err
	}
	message := "Фактическое состояние Ozon пока нельзя проверить: официальный клиент не подключён. Исходная операция сохранена; повторная отправка запрещена."
	appendProblemEvent(p, timestamp, "ozon_reconcile", "Сверка Ozon недоступна", message, "neutral", "")
	return encodeValue(state), message, false, nil
}

func finalizeOzonConfirmation(ctx context.Context, tx pgx.Tx, state map[string]any, op ozonJournalOperation, timestamp string) (bool, error) {
	if op.Purpose != "confirm_shipment" || op.Outcome != "applied" {
		return false, nil
	}
	saved := valueMapFromRaw(op.Payload)
	order := valueMap(saved["order"])
	active := valueMap(saved["assembly"])
	picks := valueArray(active["picks"])
	if err := verifyWarehousePicks(ctx, tx, op.OrderID, picks); err != nil {
		return false, err
	}
	total := 0
	for _, v := range valueArray(order["items"]) {
		total += intValue(valueMap(v)["quantity"])
	}
	if total == 0 || len(picks) != total || active["shipmentConfirmed"] != true {
		return false, stockConflict("Подтверждение Ozon не заменяет полный отбор и проверку коробки.")
	}
	related := []map[string]any{}
	for _, v := range valueArray(state["problemOrders"]) {
		e := valueMap(v)
		p := valueMap(e["problem"])
		if !sameAssemblyOrder(valueMap(e["order"]), order) || p["resolutionStatus"] == "returned" || p["closedOutcome"] != nil {
			continue
		}
		if p["resolutionStatus"] != "resolved" || p["requiresDisassembly"] == true || p["type"] == "cancelled_order" {
			return false, nil
		}
		if !reflect.DeepEqual(assemblyComposition(valueMap(e["order"])), assemblyComposition(order)) {
			return false, stockConflict("Состав заказа изменился после внешней операции.")
		}
		if p["type"] == "ozon_error" {
			linked, err := loadOzonOperation(ctx, tx, textValue(p["ozonOperationId"]))
			if err != nil {
				return false, err
			}
			if linked.Outcome != "applied" {
				return false, nil
			}
		} else if p["type"] != "insufficient_stock" {
			kind := map[string]string{"missing_in_cell": "physical_check", "damaged_product": "damage_handled", "unknown_sku": "sku_mapping", "invalid_order": "order_verified", "other": "manual_resolution"}[textValue(p["type"])]
			var verified bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assembly_problem_clearances c JOIN assembly_problem_records p ON p.id=c.problem_id WHERE c.problem_id=$1::uuid AND c.kind=$2 AND (SELECT jsonb_agg(jsonb_build_array(sku,required_quantity) ORDER BY sku) FROM assembly_order_positions WHERE version_id=c.order_version_id)=(SELECT jsonb_agg(jsonb_build_array(sku,required_quantity) ORDER BY sku) FROM assembly_order_positions WHERE version_id=p.order_version_id))`, textValue(e["problemId"]), kind).Scan(&verified); err != nil {
				return false, err
			}
			if !verified {
				return false, nil
			}
		}
		related = append(related, e)
	}
	var remaining int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(quantity),0)::int FROM assembly_order_reservations WHERE order_id=$1::uuid AND status='held'`, op.OrderID).Scan(&remaining); err != nil {
		return false, err
	}
	if remaining != 0 {
		return false, stockConflict("У собранного заказа остаётся неотобранный резерв.")
	}
	for _, v := range valueArray(state["completedOrders"]) {
		if sameAssemblyOrder(valueMap(v), order) {
			return false, nil
		}
	}
	for _, e := range related {
		p := valueMap(e["problem"])
		p["closedOutcome"] = "completed"
		appendProblemEvent(p, timestamp, "ozon_result", "Заказ завершён", "Исходное подтверждение Ozon проверено. Повторных отборов и внешней операции нет.", "success", "")
	}
	state["completedOrders"] = append(valueArray(state["completedOrders"]), order)
	if sameAssemblyOrder(valueMap(state["activeOrder"]), order) {
		state["activeOrder"] = nil
		state["activeState"] = previousEmptyState()
	}
	_, err := tx.Exec(ctx, `UPDATE assembly_order_reservations SET quantity=0,status='released',updated_at=now() WHERE order_id=$1::uuid`, op.OrderID)
	return err == nil, err
}

// Future trusted ingestion entry point. Not registered as a public HTTP endpoint.
// Receipt + domain transitions + history commit together; failure can be replayed without dispatching anything.
func ingestOzonDomainReceipt(ctx context.Context, db *pgxpool.Pool, receipt ozonDomainReceipt) error {
	if !assemblyDraftRequestID.MatchString(receipt.OperationID) || receipt.EvidenceID == "" || len(receipt.EvidenceID) > 256 {
		return stockConflict("Некорректная идентичность результата Ozon.")
	}
	observed, err := time.Parse(time.RFC3339Nano, receipt.ObservedAt)
	if err != nil {
		return stockConflict("Результат должен содержать время проверки.")
	}
	if receipt.Outcome != "unknown" && receipt.Outcome != "not_applied" && receipt.Outcome != "applied" && receipt.Outcome != "rejected" && receipt.Outcome != "cancelled" {
		return stockConflict("Неизвестный результат операции.")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var version int64
	if err = tx.QueryRow(ctx, `SELECT version FROM assembly_preview_drafts WHERE id=true FOR UPDATE`).Scan(&version); err != nil {
		return err
	}
	op, err := loadOzonOperation(ctx, tx, receipt.OperationID)
	if err != nil {
		return err
	}
	saved := valueMapFromRaw(op.Payload)
	order := valueMap(saved["order"])
	if receipt.OrderNumber != textValue(order["number"]) || receipt.PayloadHash != op.Hash {
		return stockConflict("Результат не подтверждает исходный заказ и содержимое операции.")
	}
	if len(receipt.Order) > 0 {
		var normalized assemblyorders.Order
		if err = json.Unmarshal(receipt.Order, &normalized); err != nil || normalized.Validate() != nil || normalized.Number != receipt.OrderNumber {
			return stockConflict("Некорректная проверенная версия заказа.")
		}
		receipt.Order = encodeValue(normalized)
	}
	// Store only normalized proof identity; never raw wire bodies or credentials.
	evidence := encodeValue(map[string]any{"operationId": receipt.OperationID, "number": receipt.OrderNumber, "payloadHash": receipt.PayloadHash, "outcome": receipt.Outcome, "observedAt": receipt.ObservedAt, "order": receipt.Order})
	var identical bool
	err = tx.QueryRow(ctx, `SELECT operation_id=$2::uuid AND evidence=$3::jsonb FROM assembly_ozon_observations WHERE evidence_id=$1`, receipt.EvidenceID, op.ID, []byte(evidence)).Scan(&identical)
	if err == nil {
		if !identical {
			return stockConflict("Идентификатор результата уже использован для других данных.")
		}
		return tx.Commit(ctx)
	}
	if err != pgx.ErrNoRows {
		return err
	}
	var last *time.Time
	if err = tx.QueryRow(ctx, `SELECT max(observed_at) FROM assembly_ozon_observations WHERE operation_id=$1::uuid`, op.ID).Scan(&last); err != nil {
		return err
	}
	if last != nil && !observed.After(*last) {
		return stockConflict("Устаревший результат Ozon не может заменить более новый.")
	}
	if op.Outcome == "applied" && receipt.Outcome != "applied" && receipt.Outcome != "cancelled" {
		return stockConflict("Проверенный успех нельзя заменить неизвестным или отрицательным результатом.")
	}
	if op.Outcome == "cancelled" && receipt.Outcome != "cancelled" {
		return stockConflict("Проверенную отмену нельзя снять этим результатом.")
	}
	if op.Purpose == "refresh_order" && receipt.Outcome == "applied" && len(receipt.Order) == 0 {
		return stockConflict("Успех обновления требует проверенного состава заказа.")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO assembly_ozon_observations(operation_id,evidence_id,outcome,payload_hash,evidence,observed_at) VALUES($1::uuid,$2,$3,$4,$5::jsonb,$6)`, op.ID, receipt.EvidenceID, receipt.Outcome, receipt.PayloadHash, []byte(evidence), observed); err != nil {
		return err
	}
	var raw json.RawMessage
	if err = tx.QueryRow(ctx, `SELECT assembly_load_workspace(state) FROM assembly_preview_drafts WHERE id=true`).Scan(&raw); err != nil {
		return err
	}
	state, err := decodeWorkspace(raw)
	if err != nil {
		return err
	}
	var timestamp string
	if err = tx.QueryRow(ctx, `SELECT to_char(clock_timestamp() AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')`).Scan(&timestamp); err != nil {
		return err
	}
	op.Outcome = receipt.Outcome
	if err = applyOzonReceipt(ctx, tx, state, op, receipt, timestamp); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('assembly.server_action','true',true)`); err != nil {
		return err
	}
	var compact json.RawMessage
	if err = tx.QueryRow(ctx, `SELECT assembly_store_workspace($1::jsonb)`, []byte(encodeValue(state))).Scan(&compact); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE assembly_preview_drafts SET version=$1,state=$2::jsonb,updated_at=now() WHERE id=true`, version+1, []byte(compact)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func applyOzonReceipt(ctx context.Context, tx pgx.Tx, state map[string]any, op ozonJournalOperation, receipt ozonDomainReceipt, timestamp string) error {
	saved := valueMapFromRaw(op.Payload)
	order := valueMap(saved["order"])
	active := valueMap(saved["assembly"])
	for _, v := range valueArray(state["completedOrders"]) {
		if sameAssemblyOrder(valueMap(v), order) && receipt.Outcome == "cancelled" {
			return stockConflict("Отмена после локального завершения требует отдельной обработки; автоматический откат запрещён.")
		}
	}
	for _, v := range valueArray(state["problemOrders"]) {
		e := valueMap(v)
		p := valueMap(e["problem"])
		if p["ozonOperationId"] != op.ID || p["closedOutcome"] != nil {
			continue
		}
		p["ozonOutcome"] = receipt.Outcome
		outcome := "neutral"
		if receipt.Outcome == "applied" {
			outcome = "success"
		}
		if receipt.Outcome == "rejected" {
			outcome = "failure"
		}
		appendProblemEvent(p, timestamp, "ozon_result", "Получен проверенный результат Ozon", fmt.Sprintf("Операция: %s. Результат: %s. Свидетельство: %s. Время проверки: %s.", op.Purpose, receipt.Outcome, receipt.EvidenceID, receipt.ObservedAt), outcome, "")
		if p["type"] == "ozon_error" && receipt.Outcome == "applied" {
			p["resolutionStatus"] = "resolved"
		}
	}
	if receipt.Outcome == "cancelled" {
		if enabled, e := disassemblyEnabled(ctx, tx); e != nil {
			return e
		} else if enabled {
			var exists bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assembly_disassembly_sessions WHERE order_id=$1::uuid)`, op.OrderID).Scan(&exists); e != nil {
				return e
			}
			if exists {
				return nil
			}
		}
		// Cancellation preserves picks and releases only the remaining (not physically picked) reserve.
		if sameAssemblyOrder(valueMap(state["activeOrder"]), order) {
			order = valueMap(state["activeOrder"])
			active = valueMap(state["activeState"])
		} else {
			for _, v := range valueArray(state["problemOrders"]) {
				e := valueMap(v)
				if sameAssemblyOrder(valueMap(e["order"]), order) && valueMap(e["assembly"]) != nil {
					snapshot := valueMap(e["assembly"])
					active = map[string]any{"picks": snapshot["picks"], "confirmedCell": snapshot["cell"], "confirmedCellId": snapshot["cellId"], "shipmentConfirmed": snapshot["shipmentConfirmed"]}
				}
			}
		}
		if err := verifyWarehousePicks(ctx, tx, op.OrderID, valueArray(active["picks"])); err != nil {
			return err
		}
		existing := false
		for _, v := range valueArray(state["problemOrders"]) {
			e := valueMap(v)
			if sameAssemblyOrder(valueMap(e["order"]), order) && valueMap(e["problem"])["type"] == "cancelled_order" {
				existing = true
			}
		}
		if !existing {
			snapshot := active
			if len(valueArray(active["picks"])) == 0 && !sameAssemblyOrder(valueMap(state["activeOrder"]), order) {
				snapshot = nil
			}
			next, entry, err := appendOzonCause(ctx, tx, state, order, snapshot, "cancelled_order", "Отмена подтверждена проверенным результатом Ozon.", timestamp)
			if err != nil {
				return err
			}
			for k := range state {
				delete(state, k)
			}
			for k, v := range next {
				state[k] = v
			}
			p := valueMap(entry["problem"])
			p["requiresDisassembly"] = len(valueArray(active["picks"])) > 0
			p["ozonOperationId"], p["ozonOutcome"], p["ozonPurpose"] = op.ID, "cancelled", op.Purpose
			appendProblemEvent(p, timestamp, "ozon_cancel", "Отмена заказа подтверждена", "Отборы сохранены. Освобождён только неотобранный резерв; складские остатки не изменены.", "neutral", "")
		}
		_, err := tx.Exec(ctx, `UPDATE assembly_order_reservations SET quantity=0,status='released',updated_at=now() WHERE order_id=$1::uuid`, op.OrderID)
		return err
	}
	if receipt.Outcome != "applied" {
		return nil
	}
	if op.Purpose == "confirm_shipment" {
		_, err := finalizeOzonConfirmation(ctx, tx, state, op, timestamp)
		return err
	}
	// A successful read is not authorization to pick. Changed compositions remain gated by their dedicated workflow.
	if !reflect.DeepEqual(assemblyComposition(valueMapFromRaw(receipt.Order)), assemblyComposition(order)) {
		for _, v := range valueArray(state["problemOrders"]) {
			entry := valueMap(v)
			p := valueMap(entry["problem"])
			if sameAssemblyOrder(valueMap(entry["order"]), order) && p["type"] == "invalid_order" && p["resolutionStatus"] == "awaiting" && p["closedOutcome"] == nil {
				appendProblemEvent(p, timestamp, "ozon_result", "Состав всё ещё требует согласования", "Повторная проверка данных Ozon не устранила изменение состава. Исходные отборы и резерв сохранены.", "neutral", "")
				return nil
			}
		}
		snapshot := active
		if saved["source"] == "queue" {
			snapshot = nil
		}
		next, _, err := appendOzonCause(ctx, tx, state, order, snapshot, "invalid_order", "Проверенные данные Ozon изменили состав. Требуется согласование состава, резервов и действующих отборов.", timestamp)
		if err != nil {
			return err
		}
		for k := range state {
			delete(state, k)
		}
		for k, v := range next {
			state[k] = v
		}
		return nil
	}
	result, err := readAssemblyStock(ctx, tx, order, valueArray(active["picks"]))
	if err != nil {
		return err
	}
	// Observation UUID, not the business operation key, identifies this local recheck.
	var checkRequest string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM assembly_ozon_observations WHERE evidence_id=$1`, receipt.EvidenceID).Scan(&checkRequest); err != nil {
		return err
	}
	checkID, err := recordAssemblyStock(ctx, tx, result, checkRequest, "check-stock")
	if err != nil {
		return err
	}
	updateAssemblyPlan(order, result, valueArray(active["picks"]))
	var checkedVersion string
	if err = tx.QueryRow(ctx, `SELECT assembly_store_order($1::jsonb)::text`, []byte(encodeValue(order))).Scan(&checkedVersion); err != nil {
		return err
	}
	hasStock, hasUnknown := false, false
	for _, v := range valueArray(state["problemOrders"]) {
		e := valueMap(v)
		p := valueMap(e["problem"])
		if !sameAssemblyOrder(valueMap(e["order"]), order) || p["closedOutcome"] != nil || p["resolutionStatus"] == "returned" {
			continue
		}
		e["order"] = order
		if p["type"] == "ozon_error" && p["ozonOperationId"] == op.ID {
			if _, err = tx.Exec(ctx, `INSERT INTO assembly_problem_clearances(problem_id,order_version_id,kind,evidence) VALUES($1::uuid,$2::uuid,'order_verified',$3::jsonb)`, textValue(e["problemId"]), checkedVersion, []byte(encodeValue(map[string]any{"operationId": op.ID, "evidenceId": receipt.EvidenceID, "purpose": "refresh_order", "checkId": checkID}))); err != nil {
				return err
			}
		}
		if p["type"] == "insufficient_stock" {
			hasStock = true
			applyStockResult(e, result, timestamp, checkID)
		}
		if p["type"] == "unknown_sku" {
			hasUnknown = true
		}
	}
	for _, position := range result.Positions {
		reason := ""
		if position.ProductID == 0 && !hasUnknown {
			reason = "unknown_sku"
			hasUnknown = true
		} else if position.ProductID != 0 && position.Missing > 0 && !hasStock {
			reason = "insufficient_stock"
			hasStock = true
		}
		if reason == "" {
			continue
		}
		snapshot := active
		if len(valueArray(active["picks"])) == 0 {
			snapshot = nil
		}
		next, entry, e := appendOzonCause(ctx, tx, state, order, snapshot, reason, "Причина обнаружена после проверки данных Ozon.", timestamp)
		if e != nil {
			return e
		}
		if reason == "insufficient_stock" {
			applyStockResult(entry, result, timestamp, checkID)
		}
		for k := range state {
			delete(state, k)
		}
		for k, v := range next {
			state[k] = v
		}
	}
	return nil
}

// Extends the original return gate with actual proof for the read-operation case only.
// A shipment confirmation, unknown result, or a resolved label without proof cannot return an order.
func verifiedOzonRefresh(ctx context.Context, tx pgx.Tx, problem map[string]any) (bool, error) {
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('assembly_ozon_operations') IS NOT NULL`).Scan(&enabled); err != nil {
		return false, err
	}
	if !enabled {
		return false, nil
	}
	id := textValue(problem["ozonOperationId"])
	if !assemblyDraftRequestID.MatchString(id) {
		return false, nil
	}
	op, err := loadOzonOperation(ctx, tx, id)
	if err != nil {
		return false, err
	}
	return op.Purpose == "refresh_order" && op.Outcome == "applied", nil
}
