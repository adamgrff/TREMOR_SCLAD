package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type assemblyActionConflict struct{ message string }

func (e *assemblyActionConflict) Error() string { return e.message }
func stockConflict(message string) error        { return &assemblyActionConflict{message} }

type assemblyProblemOperation struct {
	RequestID string `json:"requestId"`
	Version   int64  `json:"version"`
	ProblemID string `json:"problemId"`
	Action    string `json:"action"`
}

func assemblyProblemOperationHandler(db *pgxpool.Pool, w http.ResponseWriter, r *http.Request, action string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var body struct {
		RequestID string `json:"requestId"`
		Version   int64  `json:"version"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF || body.Version < 0 || !assemblyDraftRequestID.MatchString(body.RequestID) || !assemblyDraftRequestID.MatchString(r.PathValue("problemID")) {
		assemblyDraftError(w, 400, "invalid_problem_action", "Некорректный запрос проверки или возврата.")
		return
	}
	operation := assemblyProblemOperation{body.RequestID, body.Version, r.PathValue("problemID"), action}
	intent, _ := json.Marshal(operation)
	saveAssemblyWorkspace(db, w, r, assemblyDraftRequest{RequestID: body.RequestID, Version: body.Version, State: intent}, nil, &operation)
}

type assemblyStockPosition struct {
	SKU           string             `json:"sku"`
	Name          string             `json:"name"`
	ProductID     int64              `json:"productId"`
	Required      int                `json:"required"`
	Picked        int                `json:"picked"`
	Available     int                `json:"available"`
	Missing       int                `json:"missing"`
	Physical      int                `json:"physical"`
	OtherReserved int                `json:"otherReserved"`
	OwnReserved   int                `json:"ownReserved"`
	PreviewHeld   int                `json:"previewHeld"`
	Cells         []productStockCell `json:"cells"`
	Error         string             `json:"error,omitempty"`
}
type assemblyStockResult struct {
	OrderID   string                  `json:"orderId"`
	Positions []assemblyStockPosition `json:"positions"`
	Enough    bool                    `json:"enough"`
}

func decodeWorkspace(raw json.RawMessage) (map[string]any, error) {
	state := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &state); err != nil {
			return nil, err
		}
	}
	if len(state) == 0 {
		json.Unmarshal([]byte(`{"schemaVersion":1,"activeOrder":null,"activeState":{"picks":[],"confirmedCell":null,"shipmentConfirmed":false,"lastScan":"—","log":"","error":false},"problemOrders":[],"completedOrders":[]}`), &state)
	}
	return state, nil
}
func valueMap(value any) map[string]any { result, _ := value.(map[string]any); return result }
func valueArray(value any) []any        { result, _ := value.([]any); return result }
func textValue(value any) string        { result, _ := value.(string); return result }
func intValue(value any) int {
	switch n := value.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}
func encodeValue(value any) json.RawMessage { data, _ := json.Marshal(value); return data }

// Product locks serialize reservation writers; stock row locks also cover receiving undo.
// New stock rows can increase availability during the transaction, never invalidate a passed check.
func readAssemblyStock(ctx context.Context, tx pgx.Tx, order map[string]any, picks []any) (assemblyStockResult, error) {
	result := assemblyStockResult{Positions: []assemblyStockPosition{}, Enough: true}
	var versionID string
	if err := tx.QueryRow(ctx, `SELECT assembly_store_order($1::jsonb)::text`, []byte(encodeValue(order))).Scan(&versionID); err != nil {
		return result, err
	}
	if err := tx.QueryRow(ctx, `SELECT order_id::text FROM assembly_order_versions WHERE id=$1::uuid`, versionID).Scan(&result.OrderID); err != nil {
		return result, err
	}
	items := valueArray(order["items"])
	skus := []string{}
	for _, value := range items {
		skus = append(skus, strings.ToUpper(strings.TrimSpace(textValue(valueMap(value)["sku"]))))
	}
	rows, err := tx.Query(ctx, `SELECT id,sku,name,archived FROM products WHERE sku=ANY($1) ORDER BY id FOR NO KEY UPDATE`, skus)
	if err != nil {
		return result, err
	}
	type product struct {
		id       int64
		name     string
		archived bool
	}
	products := map[string]product{}
	ids := []int64{}
	for rows.Next() {
		var p product
		var sku string
		if err = rows.Scan(&p.id, &sku, &p.name, &p.archived); err != nil {
			rows.Close()
			return result, err
		}
		products[sku] = p
		ids = append(ids, p.id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	rows, err = tx.Query(ctx, `SELECT s.product_id,s.cell_id,c.code,w.name,s.quantity FROM cell_stock s JOIN cells c ON c.id=s.cell_id JOIN warehouses w ON w.id=c.warehouse_id WHERE s.product_id=ANY($1) ORDER BY s.product_id,s.cell_id FOR UPDATE OF s`, ids)
	if err != nil {
		return result, err
	}
	cells := map[int64][]productStockCell{}
	for rows.Next() {
		var pid int64
		var cell productStockCell
		if err = rows.Scan(&pid, &cell.CellID, &cell.CellCode, &cell.Warehouse, &cell.Quantity); err != nil {
			rows.Close()
			return result, err
		}
		if cell.Quantity > 0 {
			cells[pid] = append(cells[pid], cell)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	counts := map[string]int{}
	for _, value := range picks {
		counts[textValue(valueMap(value)["sku"])]++
	}
	for _, value := range items {
		item := valueMap(value)
		sku := textValue(item["sku"])
		p, known := products[strings.ToUpper(strings.TrimSpace(sku))]
		if counts[sku] > intValue(item["quantity"]) {
			return result, stockConflict("Сохранённые отборы превышают состав заказа. Требуется согласовать данные заказа.")
		}
		position := assemblyStockPosition{SKU: sku, Name: textValue(item["name"]), ProductID: p.id, Picked: counts[sku], Required: max(0, intValue(item["quantity"])-counts[sku]), Cells: []productStockCell{}}
		if !known || p.archived {
			position.Error = fmt.Sprintf("SKU %s не найден или товар архивирован. Проверьте сопоставление товара.", sku)
			position.Missing = position.Required
			result.Enough = false
			result.Positions = append(result.Positions, position)
			continue
		}
		position.Cells = cells[p.id]
		if position.Cells == nil {
			position.Cells = []productStockCell{}
		}
		seen := map[string]bool{}
		for _, cell := range position.Cells {
			position.Physical += cell.Quantity
			if seen[cell.CellCode] && position.Required > 0 {
				position.Error = fmt.Sprintf("Для %s найдены одинаковые коды ячеек. Уточните ячейки перед возвратом.", position.Name)
			}
			seen[cell.CellCode] = true
		}
		if err = tx.QueryRow(ctx, `SELECT COALESCE(sum(quantity) FILTER(WHERE order_id<>$2::uuid),0)::int,COALESCE(sum(quantity) FILTER(WHERE order_id=$2::uuid),0)::int FROM assembly_order_reservations WHERE product_id=$1 AND status='held'`, p.id, result.OrderID).Scan(&position.OtherReserved, &position.OwnReserved); err != nil {
			return result, err
		}
		if err = tx.QueryRow(ctx, `SELECT COALESCE(sum(quantity),0)::int FROM assembly_preview_stock_holds WHERE product_id=$1 AND order_id<>$2::uuid`, p.id, result.OrderID).Scan(&position.PreviewHeld); err != nil {
			return result, err
		}
		// Current preview picks are still physically in cell_stock. Real picks will not use this adapter.
		position.PreviewHeld += counts[sku]
		position.Available = max(0, position.Physical-position.OtherReserved-position.PreviewHeld)
		position.Missing = max(0, position.Required-position.Available)
		var physicalCheck bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assembly_problem_records p JOIN assembly_problem_affected_positions a ON a.problem_id=p.id WHERE p.order_id=$1::uuid AND p.disposition='problem' AND p.resolution_status='awaiting' AND p.problem_type IN ('missing_in_cell','damaged_product') AND a.sku=$2)`, result.OrderID, sku).Scan(&physicalCheck); err != nil {
			return result, err
		}
		if physicalCheck {
			position.Error = fmt.Sprintf("Для %s требуется физическая проверка по другой нерешённой проблеме.", position.Name)
		}
		if position.Missing > 0 || position.Error != "" {
			result.Enough = false
		}
		result.Positions = append(result.Positions, position)
	}
	return result, nil
}
func reserveAssemblyStock(ctx context.Context, tx pgx.Tx, result assemblyStockResult) error {
	for _, p := range result.Positions {
		if p.ProductID == 0 {
			return stockConflict("Нельзя зарезервировать неизвестный товар.")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO assembly_order_reservations(order_id,product_id,quantity,status) VALUES($1::uuid,$2,$3,'held') ON CONFLICT(order_id,product_id) DO UPDATE SET quantity=EXCLUDED.quantity,status='held',updated_at=now()`, result.OrderID, p.ProductID, p.Required); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO assembly_preview_stock_holds(order_id,product_id,quantity) VALUES($1::uuid,$2,$3) ON CONFLICT(order_id,product_id) DO UPDATE SET quantity=EXCLUDED.quantity,updated_at=now()`, result.OrderID, p.ProductID, p.Picked); err != nil {
			return err
		}
	}
	return nil
}
func recordAssemblyStock(ctx context.Context, tx pgx.Tx, result assemblyStockResult, requestID, purpose string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO assembly_stock_checks(order_id,request_id,purpose,result) VALUES($1::uuid,$2::uuid,$3,$4::jsonb) RETURNING id::text`, result.OrderID, requestID, purpose, []byte(encodeValue(result))).Scan(&id)
	return id, err
}
func stockResultMessage(result assemblyStockResult) string {
	for _, p := range result.Positions {
		if p.Error != "" {
			return p.Error
		}
	}
	if result.Enough {
		return "✓ Остатков достаточно. Проблема устранена!"
	}
	missing := 0
	for _, p := range result.Positions {
		missing += p.Missing
	}
	return fmt.Sprintf("Товара всё ещё недостаточно. Не хватает: %d шт.", missing)
}
func appendProblemEvent(problem map[string]any, timestamp, kind, title, message, outcome, checkID string) {
	event := map[string]any{"createdAt": timestamp, "kind": kind, "title": title, "message": message, "outcome": outcome}
	if checkID != "" {
		event["checkId"] = checkID
	}
	problem["history"] = append(valueArray(problem["history"]), event)
}
func applyStockResult(entry map[string]any, result assemblyStockResult, timestamp, checkID string) {
	problem := valueMap(entry["problem"])
	shortages := []any{}
	for _, p := range result.Positions {
		if p.Missing > 0 {
			shortages = append(shortages, map[string]any{"sku": p.SKU, "name": p.Name, "required": p.Required, "available": p.Available, "missing": p.Missing, "cells": textValue(findOrderItem(valueMap(entry["order"]), p.SKU)["cells"])})
		}
	}
	problem["shortages"] = shortages
	appendProblemEvent(problem, timestamp, "check", "Выполнена проверка остатков", "Проверка доступного количества по всем позициям заказа.", "neutral", checkID)
	title := "Товара всё ещё недостаточно"
	outcome := "failure"
	message := stockResultMessage(result)
	if result.Enough {
		title = "✓ Остатков достаточно."
		outcome = "success"
		names := []string{}
		for _, p := range result.Positions {
			names = append(names, fmt.Sprintf("%s — %d шт.", p.Name, p.Available))
		}
		message = "Проблема устранена! Доступно: " + strings.Join(names, "; ")
	}
	for _, p := range result.Positions {
		if p.Error != "" {
			title = "Ошибка проверки остатков"
			outcome = "error"
			break
		}
	}
	appendProblemEvent(problem, timestamp, "result", title, message, outcome, checkID)
	if result.Enough {
		if problem["resolutionStatus"] != "resolved" {
			appendProblemEvent(problem, timestamp, "resolution", "Проблема решена", "Остатков достаточно для продолжения сборки.", "success", checkID)
		}
		problem["resolutionStatus"] = "resolved"
	} else {
		problem["resolutionStatus"] = "awaiting"
	}
}
func findOrderItem(order map[string]any, sku string) map[string]any {
	for _, v := range valueArray(order["items"]) {
		item := valueMap(v)
		if item["sku"] == sku {
			return item
		}
	}
	return map[string]any{}
}
func updateAssemblyPlan(order map[string]any, result assemblyStockResult, picks []any) {
	for _, p := range result.Positions {
		item := findOrderItem(order, p.SKU)
		item["stock"] = p.Available
		plan := map[string]int{}
		stock := map[string]int{}
		for _, v := range picks {
			pick := valueMap(v)
			if pick["sku"] == p.SKU {
				cell := textValue(pick["cell"])
				plan[cell]++
			}
		}
		cells := append([]productStockCell{}, p.Cells...)
		sort.SliceStable(cells, func(i, j int) bool {
			if cells[i].Quantity != cells[j].Quantity {
				return cells[i].Quantity < cells[j].Quantity
			}
			if cells[i].CellCode != cells[j].CellCode {
				return cells[i].CellCode < cells[j].CellCode
			}
			return cells[i].CellID < cells[j].CellID
		})
		removedSelf := 0
		for i := range cells {
			before := cells[i].Quantity
			cells[i].Quantity = max(0, cells[i].Quantity-plan[cells[i].CellCode])
			removedSelf += before - cells[i].Quantity
		}
		unavailable := max(0, p.Physical-removedSelf-p.Available)
		for i := range cells {
			remove := min(unavailable, cells[i].Quantity)
			cells[i].Quantity -= remove
			unavailable -= remove
			stock[cells[i].CellCode] = cells[i].Quantity + plan[cells[i].CellCode]
		}
		for cell, quantity := range plan {
			if _, exists := stock[cell]; !exists {
				stock[cell] = quantity
			}
		}
		remaining := p.Required
		for _, cell := range cells {
			take := min(remaining, cell.Quantity)
			if take > 0 {
				plan[cell.CellCode] += take
			}
			remaining -= take
		}
		keys := []string{}
		for cell := range plan {
			keys = append(keys, cell)
		}
		sort.Strings(keys)
		parts := []string{}
		for _, cell := range keys {
			parts = append(parts, fmt.Sprintf("%s ×%d", cell, plan[cell]))
		}
		item["cells"] = strings.Join(parts, " · ")
		item["cellStocks"] = stock
		item["stockCells"] = p.Cells
	}
}

func applyAssemblyProblemOperation(ctx context.Context, tx pgx.Tx, current json.RawMessage, operation assemblyProblemOperation, timestamp string) (json.RawMessage, string, bool, error) {
	state, err := decodeWorkspace(current)
	if err != nil {
		return nil, "", false, err
	}
	var selected map[string]any
	for _, v := range valueArray(state["problemOrders"]) {
		entry := valueMap(v)
		if entry["problemId"] == operation.ProblemID {
			selected = entry
			break
		}
	}
	if selected == nil || valueMap(selected["problem"])["resolutionStatus"] == "returned" {
		return nil, "", false, stockConflict("Проблема уже закрыта или не найдена.")
	}
	selectedProblem := valueMap(selected["problem"])
	if operation.Action == "check-stock" && selectedProblem["type"] != "insufficient_stock" {
		return nil, "", false, stockConflict("Проверка остатков не устраняет этот тип проблемы.")
	}
	order := valueMap(selected["order"])
	related := []map[string]any{}
	snapshot := selected
	for _, v := range valueArray(state["problemOrders"]) {
		entry := valueMap(v)
		problem := valueMap(entry["problem"])
		if valueMap(entry["order"])["number"] == order["number"] && problem["resolutionStatus"] != "returned" {
			related = append(related, entry)
			created, _ := time.Parse(time.RFC3339Nano, textValue(problem["createdAt"]))
			currentCreated, _ := time.Parse(time.RFC3339Nano, textValue(valueMap(snapshot["problem"])["createdAt"]))
			if valueMap(entry["assembly"]) != nil && (valueMap(snapshot["assembly"]) == nil || created.After(currentCreated)) {
				snapshot = entry
			}
		}
	}
	for _, entry := range related {
		if !reflect.DeepEqual(assemblyComposition(valueMap(entry["order"])), assemblyComposition(order)) {
			return nil, "", false, stockConflict("Сохранённые составы заказа расходятся. Сначала устраните ошибку данных заказа.")
		}
	}
	picks := valueArray(valueMap(snapshot["assembly"])["picks"])
	if picks == nil {
		picks = []any{}
	}
	if operation.Action == "return" {
		if state["activeOrder"] != nil {
			return nil, "", false, stockConflict("Сначала завершите текущую сборку.")
		}
		for _, v := range valueArray(state["completedOrders"]) {
			if valueMap(v)["number"] == order["number"] {
				return nil, "", false, stockConflict("Заказ уже завершён.")
			}
		}
		for _, entry := range related {
			p := valueMap(entry["problem"])
			if p["resolutionStatus"] != "resolved" || p["requiresDisassembly"] == true || p["type"] == "cancelled_order" || p["type"] == "ozon_error" {
				return nil, "", false, stockConflict("Сначала устраните все проблемы заказа и завершите обязательные операции.")
			}
			if p["type"] != "insufficient_stock" {
				kind := map[string]string{"missing_in_cell": "physical_check", "damaged_product": "damage_handled", "unknown_sku": "sku_mapping", "invalid_order": "order_verified", "other": "manual_resolution"}[textValue(p["type"])]
				var verified bool
				if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assembly_problem_clearances c JOIN assembly_problem_records p ON p.id=c.problem_id WHERE c.problem_id=$1::uuid AND c.kind=$2 AND (SELECT jsonb_agg(jsonb_build_array(sku,required_quantity) ORDER BY sku) FROM assembly_order_positions WHERE version_id=c.order_version_id)=(SELECT jsonb_agg(jsonb_build_array(sku,required_quantity) ORDER BY sku) FROM assembly_order_positions WHERE version_id=p.order_version_id))`, textValue(entry["problemId"]), kind).Scan(&verified); err != nil {
					return nil, "", false, err
				}
				if !verified {
					return nil, "", false, stockConflict("Для этой причины требуется серверное подтверждение устранения; одного статуса «Решена» недостаточно.")
				}
			}
		}
	}
	result, err := readAssemblyStock(ctx, tx, order, picks)
	if err != nil {
		return nil, "", false, err
	}
	checkID, err := recordAssemblyStock(ctx, tx, result, operation.RequestID, operation.Action)
	if err != nil {
		return nil, "", false, err
	}
	for _, entry := range related {
		if valueMap(entry["problem"])["type"] == "insufficient_stock" {
			applyStockResult(entry, result, timestamp, checkID)
		}
	}
	if operation.Action == "check-stock" {
		for _, entry := range related {
			for _, p := range result.Positions {
				findOrderItem(valueMap(entry["order"]), p.SKU)["stock"] = p.Available
			}
		}
		return encodeValue(state), stockResultMessage(result), result.Enough, nil
	}
	if !result.Enough {
		// An order without a stock problem receives a separate reason, rather than overwriting another cause.
		hasStockProblem := false
		for _, entry := range related {
			if valueMap(entry["problem"])["type"] == "insufficient_stock" {
				hasStockProblem = true
			}
		}
		if !hasStockProblem {
			reason := "insufficient_stock"
			sku := ""
			for _, p := range result.Positions {
				if p.Missing > 0 || p.Error != "" {
					sku = p.SKU
					if p.ProductID == 0 {
						reason = "unknown_sku"
					}
					break
				}
			}
			creation := assemblyProblemCreationRequest{Source: "queue", Order: encodeValue(order), OrderNumber: textValue(order["number"]), Details: assemblyProblemDetails{Type: reason, PositionSKU: sku}, AllowExistingQueue: true}
			// Reuse the trusted snapshot builder for a new independent problem, keeping picks and original reasons.
			base, _ := decodeWorkspace(encodeValue(state))
			base["activeOrder"] = order
			active := map[string]any{"picks": picks, "confirmedCell": nil, "shipmentConfirmed": false, "lastScan": "—", "log": "Возврат отклонён при повторной проверке остатков.", "error": true}
			if valueMap(snapshot["assembly"]) != nil {
				base["activeState"] = active
				creation.Source = "assembly"
				creation.Order = nil
			} else {
				base["activeOrder"] = nil
				base["activeState"] = valueMap(previousEmptyState())
			}
			newState, buildErr := buildAssemblyProblem(encodeValue(base), creation, timestamp)
			if buildErr != nil {
				return nil, "", false, buildErr
			}
			state, err = decodeWorkspace(newState)
			if err != nil {
				return nil, "", false, err
			}
			entries := valueArray(state["problemOrders"])
			newEntry := valueMap(entries[len(entries)-1])
			applyStockResult(newEntry, result, timestamp, checkID)
			context := valueMap(valueMap(newEntry["problem"])["creationContext"])
			context["availabilitySource"] = "warehouse_check"
			context["checkId"] = checkID
			for _, position := range valueArray(context["positions"]) {
				for _, p := range result.Positions {
					row := valueMap(position)
					if row["sku"] == p.SKU {
						row["available"] = p.Available
						row["missing"] = p.Missing
						row["remaining"] = p.Required
						row["picked"] = p.Picked
					}
				}
			}
		}
		return encodeValue(state), "Возврат отклонён. " + stockResultMessage(result), false, nil
	}
	if err = reserveAssemblyStock(ctx, tx, result); err != nil {
		return nil, "", false, err
	}
	restoredOrder := valueMap(snapshot["order"])
	updateAssemblyPlan(restoredOrder, result, picks)
	for _, entry := range related {
		p := valueMap(entry["problem"])
		p["resolutionStatus"] = "returned"
		p["returnedAt"] = timestamp
		appendProblemEvent(p, timestamp, "return", "Заказ возвращён в сборку", "Сохранённый прогресс восстановлен после повторной серверной проверки.", "success", checkID)
	}
	state["activeOrder"] = restoredOrder
	shipment := valueMap(snapshot["assembly"])["shipmentConfirmed"] == true
	state["activeState"] = map[string]any{"picks": picks, "confirmedCell": nil, "shipmentConfirmed": shipment, "lastScan": "—", "log": "Заказ возвращён в сборку. Сохранённый прогресс восстановлен.", "error": false}
	return encodeValue(state), "Заказ возвращён в сборку.", true, nil
}

func authorizeAssemblyDraft(ctx context.Context, tx pgx.Tx, current json.RawMessage, nextRaw *json.RawMessage, requestID, timestamp string) error {
	previous, err := decodeWorkspace(current)
	if err != nil {
		return err
	}
	next, err := decodeWorkspace(*nextRaw)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(previous["problemOrders"], next["problemOrders"]) {
		return stockConflict("Проверять и возвращать проблемы должен сервер. Используйте кнопки окна проблемных заказов.")
	}
	oldOrder := valueMap(previous["activeOrder"])
	newOrder := valueMap(next["activeOrder"])
	oldCompleted := valueArray(previous["completedOrders"])
	newCompleted := valueArray(next["completedOrders"])
	completed := oldOrder != nil && newOrder == nil
	if completed {
		if len(newCompleted) != len(oldCompleted)+1 || !reflect.DeepEqual(newCompleted[:len(oldCompleted)], oldCompleted) || !reflect.DeepEqual(valueMap(newCompleted[len(oldCompleted)]), oldOrder) || valueMap(previous["activeState"])["shipmentConfirmed"] != true {
			return stockConflict("Активный заказ нельзя удалить без подтверждённого завершения.")
		}
		if _, err = tx.Exec(ctx, `UPDATE assembly_order_reservations SET quantity=0,status='released',updated_at=now() WHERE order_id IN (SELECT id FROM assembly_order_records WHERE source='preview' AND order_number=$1)`, textValue(oldOrder["number"])); err != nil {
			return err
		}
	} else if !reflect.DeepEqual(oldCompleted, newCompleted) {
		return stockConflict("Завершённые заказы нельзя переписывать.")
	}
	if newOrder != nil {
		if oldOrder != nil && !reflect.DeepEqual(oldOrder, newOrder) {
			return stockConflict("Состав активного заказа нельзя менять через снимок страницы.")
		}
		if oldOrder == nil {
			for _, v := range oldCompleted {
				if valueMap(v)["number"] == newOrder["number"] {
					return stockConflict("Завершённый заказ нельзя начать повторно.")
				}
			}
			for _, v := range valueArray(next["problemOrders"]) {
				entry := valueMap(v)
				if valueMap(entry["order"])["number"] == newOrder["number"] && valueMap(entry["problem"])["resolutionStatus"] != "returned" {
					return stockConflict("Верните проблемный заказ через серверную проверку.")
				}
			}
		}
		var reservation bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assembly_order_reservations r JOIN assembly_order_records o ON o.id=r.order_id WHERE o.source='preview' AND o.order_number=$1 AND r.status='held')`, textValue(newOrder["number"])).Scan(&reservation); err != nil {
			return err
		}
		picks := valueArray(valueMap(next["activeState"])["picks"])
		if oldOrder == nil || !reservation || !reflect.DeepEqual(valueMap(previous["activeState"])["picks"], picks) {
			if oldOrder == nil && len(picks) > 0 {
				return stockConflict("Начало сборки не создаёт отборы.")
			}
			result, readErr := readAssemblyStock(ctx, tx, newOrder, picks)
			if readErr != nil {
				return readErr
			}
			if !result.Enough {
				return stockConflict(stockResultMessage(result))
			}
			purpose := "preview-progress"
			if oldOrder == nil {
				purpose = "start"
			}
			if _, err = recordAssemblyStock(ctx, tx, result, requestID, purpose); err != nil {
				return err
			}
			if err = reserveAssemblyStock(ctx, tx, result); err != nil {
				return err
			}
			if oldOrder == nil {
				updateAssemblyPlan(newOrder, result, picks)
			}
		}
	}
	*nextRaw = encodeValue(next)
	_, err = tx.Exec(ctx, `SELECT set_config('assembly.server_action','true',true)`)
	return err
}

func previousEmptyState() any {
	var state any
	json.Unmarshal([]byte(`{"picks":[],"confirmedCell":null,"shipmentConfirmed":false,"lastScan":"—","log":"","error":false}`), &state)
	return state
}
func assemblyComposition(order map[string]any) map[string]int {
	result := map[string]int{}
	for _, v := range valueArray(order["items"]) {
		item := valueMap(v)
		result[textValue(item["sku"])] = intValue(item["quantity"])
	}
	return result
}
