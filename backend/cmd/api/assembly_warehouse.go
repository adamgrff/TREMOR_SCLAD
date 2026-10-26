package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net/http"
	"sort"
	"strings"
)

type warehousePlanEntry struct {
	CellID    int64  `json:"cellId"`
	Cell      string `json:"cell"`
	Warehouse string `json:"warehouse"`
	ScanCode  string `json:"scanCode"`
	Required  int    `json:"required"`
	Picked    int    `json:"picked"`
	Available int    `json:"available"`
}

func valueMapFromRaw(raw json.RawMessage) map[string]any {
	var value map[string]any
	json.Unmarshal(raw, &value)
	return value
}

// Physical stock already excludes warehouse picks. Only the remaining reserve is held.
func updateWarehousePlan(order map[string]any, result assemblyStockResult, picks []any) {
	allocated := map[int64]map[int64]int{}
	for _, p := range result.Positions {
		item := findOrderItem(order, p.SKU)
		item["stock"], item["productId"], item["warehouseSku"] = p.Available, p.ProductID, p.WarehouseSKU
		if allocated[p.ProductID] == nil {
			allocated[p.ProductID] = map[int64]int{}
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
		own := map[int64]int{}
		labels := map[int64]string{}
		for _, v := range picks {
			pick := valueMap(v)
			if pick["sku"] == p.SKU {
				id := int64(intValue(pick["cellId"]))
				own[id]++
				labels[id] = textValue(pick["cell"])
			}
		}
		remaining, available := p.Required, p.Available
		plans := []warehousePlanEntry{}
		parts := []string{}
		compat := map[string]int{}
		for _, c := range cells {
			eligible := min(max(0, c.Quantity-allocated[p.ProductID][c.CellID]), available)
			available -= eligible
			take := min(eligible, remaining)
			remaining -= take
			allocated[p.ProductID][c.CellID] += take
			if take == 0 && own[c.CellID] == 0 {
				continue
			}
			plan := warehousePlanEntry{c.CellID, c.CellCode, c.Warehouse, fmt.Sprintf("CELL:%d", c.CellID), take + own[c.CellID], own[c.CellID], eligible}
			plans = append(plans, plan)
			delete(own, c.CellID)
		}
		// Exhausted original cells remain visible as completed picks, identified by ID.
		ids := []int64{}
		for id := range own {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			plans = append(plans, warehousePlanEntry{CellID: id, Cell: labels[id], ScanCode: fmt.Sprintf("CELL:%d", id), Required: own[id], Picked: own[id]})
		}
		for _, plan := range plans {
			parts = append(parts, fmt.Sprintf("%s [%s] ×%d", plan.Cell, plan.ScanCode, plan.Required))
			compat[plan.ScanCode] = plan.Available + plan.Picked
		}
		item["pickingPlan"], item["cells"], item["cellStocks"], item["stockCells"] = plans, strings.Join(parts, " · "), compat, p.Cells
	}
	if expected := nextWarehouseCell(order); expected != nil {
		order["suggestedCellId"] = expected["cellId"]
	} else {
		order["suggestedCellId"] = nil
	}
}
func warehousePlans(order map[string]any) []map[string]any {
	plans := []map[string]any{}
	for _, v := range valueArray(order["items"]) {
		item := valueMap(v)
		var list []map[string]any
		json.Unmarshal(encodeValue(item["pickingPlan"]), &list)
		for _, plan := range list {
			plan["sku"] = item["sku"]
			plan["warehouseSku"] = item["warehouseSku"]
			plan["name"] = item["name"]
			plan["productId"] = item["productId"]
			plans = append(plans, plan)
		}
	}
	return plans
}
func nextWarehouseCell(order map[string]any) map[string]any {
	list := []map[string]any{}
	for _, plan := range warehousePlans(order) {
		if intValue(plan["required"]) > intValue(plan["picked"]) && intValue(plan["available"]) > 0 {
			list = append(list, plan)
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if intValue(a["available"]) != intValue(b["available"]) {
			return intValue(a["available"]) < intValue(b["available"])
		}
		if a["cell"] != b["cell"] {
			return textValue(a["cell"]) < textValue(b["cell"])
		}
		return intValue(a["cellId"]) < intValue(b["cellId"])
	})
	if len(list) == 0 {
		return nil
	}
	return list[0]
}
func loadQueuedWarehouseOrder(ctx context.Context, tx pgx.Tx, number string) (map[string]any, error) {
	var raw json.RawMessage
	err := tx.QueryRow(ctx, `SELECT assembly_load_order(h.version_id) FROM assembly_order_heads h JOIN assembly_order_records o ON o.id=h.order_id WHERE o.source='ozon' AND o.order_number=$1 AND o.status='queued' FOR UPDATE OF o,h`, number).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil, stockConflict("Заказ отсутствует в очереди или уже обрабатывается.")
	}
	if err != nil {
		return nil, err
	}
	order := map[string]any{}
	err = json.Unmarshal(raw, &order)
	return order, err
}
func prepareWarehouseProblem(ctx context.Context, tx pgx.Tx, current json.RawMessage, request *assemblyProblemCreationRequest) error {
	state, err := decodeWorkspace(current)
	if err != nil {
		return err
	}
	order := valueMap(state["activeOrder"])
	picks := valueArray(valueMap(state["activeState"])["picks"])
	if request.Source == "queue" {
		if len(request.Order) > 0 {
			return stockConflict("Состав реального заказа передаёт только сервер.")
		}
		order, err = loadQueuedWarehouseOrder(ctx, tx, request.OrderNumber)
		picks = []any{}
		if err != nil {
			return err
		}
	}
	if order["source"] != "ozon" {
		return stockConflict("Историческая демонстрационная сборка не выполняет складские операции.")
	}
	result, err := readAssemblyStock(ctx, tx, order, picks)
	if err != nil {
		return err
	}
	if err = verifyWarehousePicks(ctx, tx, result.OrderID, picks); err != nil {
		return err
	}
	// The snapshot builder receives current observations without changing the active immutable composition.
	updateWarehousePlan(order, result, picks)
	if request.Source == "queue" {
		request.Order = encodeValue(order)
	} else {
		// The caller uses this hydrated state for creation; see buildWarehouseProblem.
		request.Order = encodeValue(order)
	}
	return nil
}
func warehouseAssemblyHandler(db *pgxpool.Pool, w http.ResponseWriter, r *http.Request, action string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var body struct {
		RequestID   string `json:"requestId"`
		Version     int64  `json:"version"`
		OrderNumber string `json:"orderNumber,omitempty"`
		Code        string `json:"code,omitempty"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if dec.Decode(&body) != nil || dec.Decode(new(any)) != io.EOF || body.Version < 0 || !assemblyDraftRequestID.MatchString(body.RequestID) || (action == "start" && body.OrderNumber == "") || (action == "scan" && (strings.TrimSpace(body.Code) == "" || len(body.Code) > 256)) || (action != "start" && body.OrderNumber != "") || (action != "scan" && body.Code != "") {
		assemblyDraftError(w, 400, "invalid_assembly_action", "Некорректное действие сборки.")
		return
	}
	op := assemblyProblemOperation{RequestID: body.RequestID, Version: body.Version, Action: action, OrderNumber: body.OrderNumber, Code: strings.TrimSpace(body.Code)}
	saveAssemblyWorkspace(db, w, r, assemblyDraftRequest{RequestID: body.RequestID, Version: body.Version, State: encodeValue(op)}, nil, &op)
}
func applyWarehouseAssemblyOperation(ctx context.Context, tx pgx.Tx, current json.RawMessage, op assemblyProblemOperation, timestamp string) (json.RawMessage, string, bool, error) {
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('assembly_warehouse_operations') IS NOT NULL`).Scan(&enabled); err != nil {
		return nil, "", false, err
	}
	if !enabled {
		return nil, "", false, stockConflict("Примените миграцию 015.")
	}
	state, err := decodeWorkspace(current)
	if err != nil {
		return nil, "", false, err
	}
	order := valueMap(state["activeOrder"])
	active := valueMap(state["activeState"])
	picks := valueArray(active["picks"])
	if picks == nil {
		picks = []any{}
	}
	if op.Action == "start" {
		if order != nil {
			return nil, "", false, stockConflict("Сначала завершите активную сборку.")
		}
		order, err = loadQueuedWarehouseOrder(ctx, tx, op.OrderNumber)
		if err != nil {
			return nil, "", false, err
		}
		result, e := readAssemblyStock(ctx, tx, order, []any{})
		if e != nil {
			return nil, "", false, e
		}
		if _, e = recordAssemblyStock(ctx, tx, result, op.RequestID, "start"); e != nil {
			return nil, "", false, e
		}
		if !result.Enough {
			return encodeValue(state), stockResultMessage(result), false, nil
		}
		if e = reserveAssemblyStock(ctx, tx, result); e != nil {
			return nil, "", false, e
		}
		updateWarehousePlan(order, result, []any{})
		state["activeOrder"], state["activeState"] = order, valueMap(previousEmptyState())
		return encodeValue(state), "Сборка начата.", true, nil
	}
	if order == nil || order["source"] != "ozon" {
		return nil, "", false, stockConflict("Нет активной сборки реального заказа. Демонстрационные отборы автоматически не списываются.")
	}
	var oid, vid string
	if err = tx.QueryRow(ctx, `SELECT assembly_store_order($1::jsonb)::text`, []byte(encodeValue(order))).Scan(&vid); err != nil {
		return nil, "", false, err
	}
	if err = tx.QueryRow(ctx, `SELECT order_id::text FROM assembly_order_versions WHERE id=$1::uuid`, vid).Scan(&oid); err != nil {
		return nil, "", false, err
	}
	// Persisted picks must match the actual, un-reversed ledger before any mutation.
	if err = verifyWarehousePicks(ctx, tx, oid, picks); err != nil {
		return nil, "", false, err
	}
	total := 0
	for _, item := range valueArray(order["items"]) {
		total += intValue(valueMap(item)["quantity"])
	}
	fail := func(message string) (json.RawMessage, string, bool, error) {
		active["error"] = true
		active["log"] = message
		return encodeValue(state), message, false, nil
	}
	if op.Action == "finish" {
		if len(picks) != total || active["shipmentConfirmed"] != true {
			return nil, "", false, stockConflict("Соберите все товары и подтвердите отправление.")
		}
		var journal bool
		if err = tx.QueryRow(ctx, `SELECT to_regclass('assembly_ozon_operations') IS NOT NULL`).Scan(&journal); err != nil {
			return nil, "", false, err
		}
		if journal {
			return prepareOzonConfirmation(ctx, tx, state, order, active, op, timestamp)
		}
		// A label scan proves the physical box, not successful marketplace confirmation.
		// Stage 7 will supply the verified external operation; never fabricate completion.
		active["log"] = "Товары собраны, коробка подтверждена. Для завершения нужно подтверждение Ozon; внешний клиент ещё не подключён."
		active["error"] = false
		return encodeValue(state), textValue(active["log"]), false, nil
	}
	if op.Action == "undo" {
		if len(picks) == 0 {
			return nil, "", false, stockConflict("Нет отбора для отмены.")
		}
		last := valueMap(picks[len(picks)-1])
		operation := textValue(last["operationId"])
		var cellID, productID int64
		var positionID string
		if err = tx.QueryRow(ctx, `SELECT cell_id,product_id,order_position_id::text FROM assembly_warehouse_operations WHERE id=$1::uuid AND order_id=$2::uuid AND kind='pick'`, operation, oid).Scan(&cellID, &productID, &positionID); err != nil {
			return nil, "", false, err
		}
		var locked int64
		if err = tx.QueryRow(ctx, `SELECT id FROM products WHERE id=$1 FOR NO KEY UPDATE`, productID).Scan(&locked); err != nil {
			return nil, "", false, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO assembly_warehouse_operations(request_id,order_id,order_position_id,product_id,cell_id,kind,quantity,reverses_operation_id) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5,'undo_pick',1,$6::uuid)`, op.RequestID, oid, positionID, productID, cellID, operation); err != nil {
			return nil, "", false, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO cell_stock(cell_id,product_id,quantity) VALUES($1,$2,1) ON CONFLICT(cell_id,product_id) DO UPDATE SET quantity=cell_stock.quantity+1`, cellID, productID); err != nil {
			return nil, "", false, err
		}
		reserved, reserveErr := tx.Exec(ctx, `UPDATE assembly_order_reservations SET quantity=quantity+1,status='held',updated_at=now() WHERE order_id=$1::uuid AND product_id=$2`, oid, productID)
		if reserveErr != nil {
			return nil, "", false, reserveErr
		}
		if reserved.RowsAffected() != 1 {
			return nil, "", false, stockConflict("Отсутствует резерв для восстановления отменённого отбора.")
		}
		picks = picks[:len(picks)-1]
		active["picks"], active["shipmentConfirmed"] = picks, false
		result, e := readAssemblyStock(ctx, tx, order, picks)
		if e != nil {
			return nil, "", false, e
		}
		updateWarehousePlan(order, result, picks)
		active["confirmedCell"], active["confirmedCellId"] = last["cell"], last["cellId"]
		active["error"], active["log"] = false, fmt.Sprintf("Отменён последний отбор: %s, 1 шт. из %s.", last["sku"], last["cell"])
		return encodeValue(state), textValue(active["log"]), true, nil
	}
	if active["shipmentConfirmed"] == true {
		return fail("Отправление уже подтверждено.")
	}
	if len(picks) == total {
		if op.Code != textValue(order["number"]) {
			return fail("Неверный код отправления. Проверьте этикетку Ozon.")
		}
		active["shipmentConfirmed"], active["confirmedCell"], active["confirmedCellId"], active["lastScan"], active["error"], active["log"] = true, nil, nil, op.Code, false, "Коробка подтверждена. Можно завершить сборку."
		return encodeValue(state), textValue(active["log"]), true, nil
	}
	// Re-read balances at every scan. An old cell confirmation never permits an invalid pick.
	result, e := readAssemblyStock(ctx, tx, order, picks)
	if e != nil {
		return nil, "", false, e
	}
	updateWarehousePlan(order, result, picks)
	confirmed := int64(intValue(active["confirmedCellId"]))
	if confirmed == 0 {
		expected := nextWarehouseCell(order)
		if expected == nil {
			return fail("Нет доступной ячейки. Создайте проблему и проверьте остатки.")
		}
		cellID := int64(intValue(expected["cellId"]))
		matched := op.Code == textValue(expected["scanCode"])
		if !matched && op.Code == textValue(expected["cell"]) {
			var count int
			if err = tx.QueryRow(ctx, `SELECT count(*) FROM cells WHERE code=$1`, op.Code).Scan(&count); err != nil {
				return nil, "", false, err
			}
			matched = count == 1
		}
		if !matched {
			return fail(fmt.Sprintf("Неверная или неоднозначная ячейка. Ожидается %s (%s).", expected["cell"], expected["scanCode"]))
		}
		active["confirmedCell"], active["confirmedCellId"], active["lastScan"], active["error"], active["log"] = expected["cell"], cellID, op.Code, false, "Ячейка подтверждена. Сканируйте любой товар из списка."
		return encodeValue(state), textValue(active["log"]), true, nil
	}
	var chosen map[string]any
	for _, plan := range warehousePlans(order) {
		if int64(intValue(plan["cellId"])) == confirmed && strings.EqualFold(warehouseScanSKU(plan), op.Code) && intValue(plan["picked"]) < intValue(plan["required"]) && intValue(plan["available"]) > 0 {
			chosen = plan
			break
		}
	}
	if chosen == nil {
		return fail("Товар не требуется из подтверждённой ячейки, недоступен или уже собран.")
	}
	productID := int64(intValue(chosen["productId"]))
	if !result.Enough {
		return fail("Остатки изменились. Создайте проблему для проверки заказа.")
	}
	changed, e := tx.Exec(ctx, `UPDATE cell_stock SET quantity=quantity-1 WHERE cell_id=$1 AND product_id=$2 AND quantity>0`, confirmed, productID)
	if e != nil {
		return nil, "", false, e
	}
	if changed.RowsAffected() != 1 {
		return nil, "", false, stockConflict("Товар закончился в ячейке.")
	}
	changed, e = tx.Exec(ctx, `UPDATE assembly_order_reservations SET quantity=quantity-1,updated_at=now() WHERE order_id=$1::uuid AND product_id=$2 AND status='held' AND quantity>0`, oid, productID)
	if e != nil {
		return nil, "", false, e
	}
	if changed.RowsAffected() != 1 {
		return nil, "", false, stockConflict("Отсутствует оставшийся резерв для отбора.")
	}
	var operationID, positionID, occurred, pickID, referenceID string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM assembly_order_positions WHERE version_id=$1::uuid AND sku=$2`, vid, textValue(chosen["sku"])).Scan(&positionID); err != nil {
		return nil, "", false, err
	}
	if err = tx.QueryRow(ctx, `INSERT INTO assembly_warehouse_operations(request_id,order_id,order_position_id,product_id,cell_id,kind,quantity) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5,'pick',1) RETURNING id::text,to_char(occurred_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')`, op.RequestID, oid, positionID, productID, confirmed).Scan(&operationID, &occurred); err != nil {
		return nil, "", false, err
	}
	if err = tx.QueryRow(ctx, `INSERT INTO assembly_cell_references(scope,display_code,warehouse_cell_id) VALUES('warehouse:'||$1::bigint::text,$2,$1::bigint) ON CONFLICT(warehouse_cell_id) DO UPDATE SET warehouse_cell_id=EXCLUDED.warehouse_cell_id RETURNING id::text`, confirmed, textValue(chosen["cell"])).Scan(&referenceID); err != nil {
		return nil, "", false, err
	}
	if err = tx.QueryRow(ctx, `INSERT INTO assembly_pick_records(order_id,position_sku,order_position_id,cell_reference_id,source,warehouse_operation_id,snapshot_key,quantity,occurred_at) VALUES($1::uuid,$2,$3::uuid,$4::uuid,'warehouse_operation',$5::uuid,$5::text,1,$6::timestamptz) RETURNING id::text`, oid, textValue(chosen["sku"]), positionID, referenceID, operationID, occurred).Scan(&pickID); err != nil {
		return nil, "", false, err
	}
	picks = append(picks, map[string]any{"sku": chosen["sku"], "cell": chosen["cell"], "cellId": confirmed, "operationId": operationID, "pickId": pickID, "occurredAt": occurred})
	active["picks"] = picks
	result, e = readAssemblyStock(ctx, tx, order, picks)
	if e != nil {
		return nil, "", false, e
	}
	updateWarehousePlan(order, result, picks)
	remains := false
	for _, plan := range warehousePlans(order) {
		if int64(intValue(plan["cellId"])) == confirmed && intValue(plan["picked"]) < intValue(plan["required"]) {
			remains = true
		}
	}
	if !remains {
		active["confirmedCell"], active["confirmedCellId"] = nil, nil
	}
	active["lastScan"], active["error"], active["log"] = op.Code, false, fmt.Sprintf("%s: подтверждена 1 шт. из %s.", chosen["name"], chosen["cell"])
	return encodeValue(state), textValue(active["log"]), true, nil
}
func verifyWarehousePicks(ctx context.Context, tx pgx.Tx, orderID string, picks []any) error {
	var actual []string
	query := `SELECT op.id::text FROM assembly_warehouse_operations op WHERE op.order_id=$1::uuid AND op.kind='pick' AND NOT EXISTS(SELECT 1 FROM assembly_warehouse_operations undo WHERE undo.reverses_operation_id=op.id)`
	if enabled, e := disassemblyEnabled(ctx, tx); e != nil {
		return e
	} else if enabled {
		returnsRelation, e := disassemblyReturnsRelation(ctx, tx)
		if e != nil {
			return e
		}
		query += ` AND NOT EXISTS(SELECT 1 FROM assembly_disassembly_items i JOIN ` + returnsRelation + ` r ON r.item_id=i.id WHERE i.original_operation_id=op.id)`
	}
	query += " ORDER BY op.occurred_at,op.id"
	rows, err := tx.Query(ctx, query, orderID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return err
		}
		actual = append(actual, id)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if len(actual) != len(picks) {
		return stockConflict("Прогресс расходится с действующими складскими операциями.")
	}
	for i, id := range actual {
		if textValue(valueMap(picks[i])["operationId"]) != id {
			return stockConflict("Порядок отборов расходится со складскими операциями.")
		}
	}
	return nil
}
func enrichWarehouseProblemHistory(ctx context.Context, tx pgx.Tx, raw json.RawMessage) (json.RawMessage, error) {
	state, err := decodeWorkspace(raw)
	if err != nil {
		return nil, err
	}
	entries := valueArray(state["problemOrders"])
	entry := valueMap(entries[len(entries)-1])
	order := valueMap(entry["order"])
	if order["source"] != "ozon" {
		return raw, nil
	}
	rows, err := tx.Query(ctx, `SELECT op.id::text,op.kind,p.sku,c.code,op.cell_id,to_char(op.occurred_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),COALESCE(op.reverses_operation_id::text,'') FROM assembly_warehouse_operations op JOIN assembly_order_records o ON o.id=op.order_id JOIN assembly_order_positions p ON p.id=op.order_position_id JOIN cells c ON c.id=op.cell_id WHERE o.source='ozon' AND o.order_number=$1 ORDER BY op.occurred_at,op.id`, textValue(order["number"]))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	history := []any{}
	operations := []any{}
	counts := map[string]int{}
	for rows.Next() {
		var id, kind, sku, cell, timestamp, reversed string
		var cellID int64
		if err = rows.Scan(&id, &kind, &sku, &cell, &cellID, &timestamp, &reversed); err != nil {
			return nil, err
		}
		title := "Подтверждён отбор"
		if kind == "undo_pick" {
			title = "Отменён последний отбор"
			counts[sku]--
		} else {
			counts[sku]++
		}
		history = append(history, map[string]any{"createdAt": timestamp, "kind": kind, "title": title, "message": fmt.Sprintf("Ячейка: %s [ID %d]\nПозиция: %s\nСобрано: %d / %d", cell, cellID, sku, counts[sku], intValue(findOrderItem(order, sku)["quantity"])), "outcome": "neutral", "operationId": id})
		operations = append(operations, map[string]any{"operationId": id, "kind": kind, "sku": sku, "cellId": cellID, "cell": cell, "occurredAt": timestamp, "reversesOperationId": reversed})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	problem := valueMap(entry["problem"])
	for _, event := range valueArray(problem["history"]) {
		if valueMap(event)["kind"] != "pick" {
			history = append(history, event)
		}
	}
	problem["history"] = history
	valueMap(problem["creationContext"])["warehouseOperations"] = operations
	return encodeValue(state), nil
}

func warehouseScanSKU(plan map[string]any) string {
	if sku := textValue(plan["warehouseSku"]); sku != "" {
		return sku
	}
	return textValue(plan["sku"])
}
