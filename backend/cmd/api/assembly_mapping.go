package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func assemblyMappingHandler(db *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var body struct {
		RequestID   string `json:"requestId"`
		Version     int64  `json:"version"`
		PositionSKU string `json:"positionSku"`
		ProductID   int64  `json:"productId"`
		Confirmed   bool   `json:"confirmed"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF || body.Version < 0 || !assemblyDraftRequestID.MatchString(body.RequestID) || !assemblyDraftRequestID.MatchString(r.PathValue("problemID")) || body.ProductID <= 0 || strings.TrimSpace(body.PositionSKU) == "" || !body.Confirmed {
		assemblyDraftError(w, 400, "invalid_mapping", "Выберите позицию и товар и подтвердите их соответствие.")
		return
	}
	op := assemblyProblemOperation{RequestID: body.RequestID, Version: body.Version, ProblemID: r.PathValue("problemID"), Action: "map-product", PositionSKU: body.PositionSKU, ProductID: body.ProductID, Confirmed: body.Confirmed}
	saveAssemblyWorkspace(db, w, r, assemblyDraftRequest{RequestID: op.RequestID, Version: op.Version, State: encodeValue(op)}, nil, &op)
}

func assemblyMappingProductsHandler(db *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query(r.Context(), `SELECT id,sku,name FROM products WHERE NOT archived ORDER BY name,sku,id`)
	if err != nil {
		assemblyDraftFailure(w, err)
		return
	}
	defer rows.Close()
	products := []any{}
	for rows.Next() {
		var id int64
		var sku, name string
		if err = rows.Scan(&id, &sku, &name); err != nil {
			assemblyDraftFailure(w, err)
			return
		}
		products = append(products, map[string]any{"id": id, "sku": sku, "name": name})
	}
	if err = rows.Err(); err != nil {
		assemblyDraftFailure(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]any{"products": products})
}

// Uses the same workspace transaction, version guard and request replay as picking.
// Resolving a SKU cause and authorizing an order to resume are separate decisions.
func applyAssemblyMapping(ctx context.Context, tx pgx.Tx, current json.RawMessage, op assemblyProblemOperation, timestamp string) (json.RawMessage, string, bool, error) {
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('assembly_sku_mappings') IS NOT NULL`).Scan(&enabled); err != nil {
		return nil, "", false, err
	}
	if !enabled {
		return nil, "", false, stockConflict("Примените миграцию 016.")
	}
	state, err := decodeWorkspace(current)
	if err != nil {
		return nil, "", false, err
	}
	var selected map[string]any
	for _, v := range valueArray(state["problemOrders"]) {
		e := valueMap(v)
		if e["problemId"] == op.ProblemID {
			selected = e
			break
		}
	}
	if selected == nil {
		return nil, "", false, stockConflict("Проблема не найдена.")
	}
	problem := valueMap(selected["problem"])
	order := valueMap(selected["order"])
	if order["source"] != "ozon" || problem["type"] != "unknown_sku" || problem["resolutionStatus"] != "awaiting" {
		return nil, "", false, stockConflict("Сопоставление доступно для открытой проблемы SKU реального заказа.")
	}
	item := findOrderItem(order, op.PositionSKU)
	affected := false
	for _, v := range valueArray(valueMap(problem["creationContext"])["affectedPositions"]) {
		if valueMap(v)["sku"] == op.PositionSKU {
			affected = true
		}
	}
	if !affected || len(item) == 0 || !op.Confirmed {
		return nil, "", false, stockConflict("Позиция не относится к этой проблеме.")
	}
	related := []map[string]any{}
	snapshot := selected
	for _, v := range valueArray(state["problemOrders"]) {
		e := valueMap(v)
		p := valueMap(e["problem"])
		if !sameAssemblyOrder(valueMap(e["order"]), order) || p["resolutionStatus"] == "returned" {
			continue
		}
		if !reflect.DeepEqual(assemblyComposition(valueMap(e["order"])), assemblyComposition(order)) {
			return nil, "", false, stockConflict("Составы заказа расходятся. Сначала проверьте данные заказа.")
		}
		related = append(related, e)
		created, _ := time.Parse(time.RFC3339Nano, textValue(p["createdAt"]))
		previous, _ := time.Parse(time.RFC3339Nano, textValue(valueMap(snapshot["problem"])["createdAt"]))
		if valueMap(e["assembly"]) != nil && (valueMap(snapshot["assembly"]) == nil || created.After(previous)) {
			snapshot = e
		}
	}
	picks := valueArray(valueMap(snapshot["assembly"])["picks"])
	if picks == nil {
		picks = []any{}
	}
	var oid, key string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM assembly_order_records WHERE source='ozon' AND order_number=$1 AND status='problem'`, textValue(order["number"])).Scan(&oid); err == pgx.ErrNoRows {
		return nil, "", false, stockConflict("Заказ больше не находится в проблемных.")
	} else if err != nil {
		return nil, "", false, err
	}
	if err = verifyWarehousePicks(ctx, tx, oid, picks); err != nil {
		return nil, "", false, err
	}
	for _, v := range valueArray(order["items"]) {
		i := valueMap(v)
		if intValue(i["quantity"]) <= 0 || textValue(i["sku"]) == "" {
			return nil, "", false, stockConflict("Некорректный состав заказа.")
		}
	}
	if err = tx.QueryRow(ctx, `SELECT assembly_external_key($1::jsonb)`, []byte(encodeValue(item))).Scan(&key); err != nil {
		return nil, "", false, err
	}
	// Never retarget a recorded position or an existing mapping, including in another order.
	var incompatible bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assembly_order_positions p JOIN assembly_order_versions v ON v.id=p.version_id JOIN assembly_order_records o ON o.id=v.order_id WHERE o.source='ozon' AND assembly_external_key(p.payload)=$1 AND p.warehouse_product_id IS NOT NULL AND p.warehouse_product_id<>$2) OR EXISTS(SELECT 1 FROM assembly_sku_mappings WHERE external_key=$1 AND product_id<>$2)`, key, op.ProductID).Scan(&incompatible); err != nil {
		return nil, "", false, err
	}
	if incompatible {
		return nil, "", false, stockConflict("Этот SKU уже связан с другим товаром. Переназначение существующих позиций и отборов запрещено.")
	}
	var valid bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM products WHERE id=$1 AND NOT archived)`, op.ProductID).Scan(&valid); err != nil {
		return nil, "", false, err
	}
	if !valid {
		return nil, "", false, stockConflict("Товар не найден или архивирован.")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO assembly_sku_mappings(external_key,product_id,problem_id,request_id) VALUES($1,$2,$3::uuid,$4::uuid) ON CONFLICT(external_key) DO NOTHING`, key, op.ProductID, op.ProblemID, op.RequestID); err != nil {
		return nil, "", false, err
	}
	result, err := readAssemblyStock(ctx, tx, order, picks)
	if err != nil {
		return nil, "", false, err
	}
	// Locks acquired by the stock check also protect against concurrent archival.
	for _, p := range result.Positions {
		if p.SKU == op.PositionSKU && (p.ProductID != op.ProductID || p.WarehouseSKU == "") {
			return nil, "", false, stockConflict("Сопоставленный товар недоступен.")
		}
	}
	if err = tx.QueryRow(ctx, `SELECT NOT archived FROM products WHERE id=$1`, op.ProductID).Scan(&valid); err != nil {
		return nil, "", false, err
	}
	if !valid {
		return nil, "", false, stockConflict("Товар архивирован во время сопоставления.")
	}
	checkID, err := recordAssemblyStock(ctx, tx, result, op.RequestID, "check-stock")
	if err != nil {
		return nil, "", false, err
	}
	updateAssemblyPlan(order, result, picks)
	var vid string
	if err = tx.QueryRow(ctx, `SELECT assembly_store_order($1::jsonb)::text`, []byte(encodeValue(order))).Scan(&vid); err != nil {
		return nil, "", false, err
	}
	appendProblemEvent(problem, timestamp, "mapping", "Товар сопоставлен", fmt.Sprintf("%s → %s. Соответствие подтверждено оператором; проверены все позиции заказа.", op.PositionSKU, findOrderItem(order, op.PositionSKU)["warehouseSku"]), "success", checkID)
	checkMessage := stockResultMessage(result)
	if result.Enough {
		checkMessage = "Остатков достаточно по всему заказу. Другие причины и подтверждения проверяются независимо."
	}
	for _, entry := range related {
		entry["order"] = order
		p := valueMap(entry["problem"])
		if p["type"] == "insufficient_stock" {
			applyStockResult(entry, result, timestamp, checkID)
			continue
		}
		appendProblemEvent(p, timestamp, "check", "Заказ повторно проверен", checkMessage, map[bool]string{true: "success", false: "failure"}[result.Enough], checkID)
		if p["type"] != "unknown_sku" {
			continue
		}
		allMapped := true
		evidence := []any{}
		positions := valueArray(valueMap(p["creationContext"])["affectedPositions"])
		if len(positions) == 0 {
			allMapped = false
		}
		for _, v := range positions {
			sku := textValue(valueMap(v)["sku"])
			matched := false
			for _, position := range result.Positions {
				if position.SKU != sku {
					continue
				}
				var active bool
				if position.ProductID != 0 {
					if err = tx.QueryRow(ctx, `SELECT NOT archived FROM products WHERE id=$1`, position.ProductID).Scan(&active); err != nil {
						return nil, "", false, err
					}
				}
				matched = active
				evidence = append(evidence, map[string]any{"sku": sku, "productId": position.ProductID})
			}
			allMapped = allMapped && matched
		}
		if allMapped && p["resolutionStatus"] != "resolved" {
			if _, err = tx.Exec(ctx, `INSERT INTO assembly_problem_clearances(problem_id,order_version_id,kind,evidence) VALUES($1::uuid,$2::uuid,'sku_mapping',$3::jsonb)`, textValue(entry["problemId"]), vid, []byte(encodeValue(map[string]any{"requestId": op.RequestID, "checkId": checkID, "positions": evidence, "operatorConfirmed": true}))); err != nil {
				return nil, "", false, err
			}
			p["resolutionStatus"] = "resolved"
			appendProblemEvent(p, timestamp, "resolution", "Проблема SKU решена", "Все затронутые SKU сопоставлены; состав и количества проверены. Возврат требует отдельного действия и проверки всех причин.", "success", checkID)
		}
	}
	// Register a separate shortage, rather than turning a SKU cause into a stock cause.
	stockResult := assemblyStockResult{OrderID: result.OrderID, Positions: []assemblyStockPosition{}, Enough: true}
	firstMissing := ""
	for _, p := range result.Positions {
		if p.ProductID == 0 {
			continue
		}
		stockResult.Positions = append(stockResult.Positions, p)
		if p.Missing > 0 {
			stockResult.Enough = false
			if firstMissing == "" {
				firstMissing = p.SKU
			}
		}
	}
	hasStock := false
	for _, e := range related {
		if valueMap(e["problem"])["type"] == "insufficient_stock" {
			hasStock = true
		}
	}
	if !hasStock && firstMissing != "" {
		originalActive, originalState := state["activeOrder"], state["activeState"]
		base, _ := decodeWorkspace(encodeValue(state))
		creation := assemblyProblemCreationRequest{Source: "queue", OrderNumber: textValue(order["number"]), Order: encodeValue(order), AllowExistingQueue: true, Details: assemblyProblemDetails{Type: "insufficient_stock", PositionSKU: firstMissing}}
		if valueMap(snapshot["assembly"]) != nil {
			base["activeOrder"] = order
			saved := valueMap(snapshot["assembly"])
			base["activeState"] = map[string]any{"picks": picks, "confirmedCell": saved["cell"], "confirmedCellId": saved["cellId"], "shipmentConfirmed": saved["shipmentConfirmed"], "lastScan": "—", "log": "Недостаток обнаружен после сопоставления.", "error": true}
			creation.Source = "assembly"
			creation.Order = nil
		}
		next, e := buildAssemblyProblem(encodeValue(base), creation, timestamp)
		if e != nil {
			return nil, "", false, e
		}
		next, e = enrichWarehouseProblemHistory(ctx, tx, next)
		if e != nil {
			return nil, "", false, e
		}
		state, e = decodeWorkspace(next)
		if e != nil {
			return nil, "", false, e
		}
		state["activeOrder"], state["activeState"] = originalActive, originalState
		entries := valueArray(state["problemOrders"])
		entry := valueMap(entries[len(entries)-1])
		applyStockResult(entry, stockResult, timestamp, checkID)
		context := valueMap(valueMap(entry["problem"])["creationContext"])
		context["availabilitySource"] = "warehouse_check"
		context["checkId"] = checkID
	}
	blockers := 0
	for _, v := range valueArray(state["problemOrders"]) {
		e := valueMap(v)
		p := valueMap(e["problem"])
		if sameAssemblyOrder(valueMap(e["order"]), order) && p["resolutionStatus"] != "returned" && (p["resolutionStatus"] != "resolved" || p["requiresDisassembly"] == true || p["type"] == "cancelled_order" || p["type"] == "ozon_error") {
			blockers++
		}
	}
	message := "Сопоставление сохранено. Заказ проверен; возврат в сборку выполняется отдельно."
	if blockers > 0 || !result.Enough {
		message = fmt.Sprintf("Сопоставление сохранено. Возврат запрещён: нерешённых причин — %d. %s", blockers, stockResultMessage(result))
	}
	return encodeValue(state), message, true, nil
}
