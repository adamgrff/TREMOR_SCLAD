package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func disassemblyHandler(db *pgxpool.Pool, w http.ResponseWriter, r *http.Request, action string) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var body struct {
		RequestID string `json:"requestId"`
		Version   int64  `json:"version"`
		Code      string `json:"code,omitempty"`
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(&body) != nil || d.Decode(new(any)) != io.EOF || !assemblyDraftRequestID.MatchString(body.RequestID) || !assemblyDraftRequestID.MatchString(r.PathValue("sessionID")) || body.Version < 0 || (action == "disassembly-scan" && (strings.TrimSpace(body.Code) == "" || len(body.Code) > 256)) || (action != "disassembly-scan" && body.Code != "") {
		assemblyDraftError(w, 400, "invalid_disassembly", "Некорректное действие разбора.")
		return
	}
	op := assemblyProblemOperation{RequestID: body.RequestID, Version: body.Version, SessionID: r.PathValue("sessionID"), Action: action, Code: strings.TrimSpace(body.Code)}
	saveAssemblyWorkspace(db, w, r, assemblyDraftRequest{RequestID: op.RequestID, Version: op.Version, State: encodeValue(op)}, nil, &op)
}

func disassemblyEnabled(ctx context.Context, tx pgx.Tx) (bool, error) {
	var enabled bool
	err := tx.QueryRow(ctx, `SELECT to_regclass('assembly_disassembly_sessions') IS NOT NULL`).Scan(&enabled)
	return enabled, err
}

// Before 019 there cannot be reversals, so the legacy ledger is effective in
// its entirety. After 019 all current-state reads use the shared database view.
func disassemblyReturnsRelation(ctx context.Context, tx pgx.Tx) (string, error) {
	var effective bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('assembly_disassembly_effective_returns') IS NOT NULL`).Scan(&effective); err != nil {
		return "", err
	}
	if effective {
		return "assembly_disassembly_effective_returns", nil
	}
	return "assembly_disassembly_returns", nil
}

func appendDisassemblyEvent(state map[string]any, order map[string]any, timestamp, kind, title, message string) {
	for _, v := range valueArray(state["problemOrders"]) {
		entry := valueMap(v)
		p := valueMap(entry["problem"])
		if sameAssemblyOrder(valueMap(entry["order"]), order) && p["resolutionStatus"] != "returned" && p["closedOutcome"] == nil {
			appendProblemEvent(p, timestamp, kind, title, message, "neutral", "")
		}
	}
}

func transferDisassembly(ctx context.Context, tx pgx.Tx, state map[string]any, request assemblyProblemOperation, timestamp string) (json.RawMessage, string, bool, error) {
	var entry map[string]any
	for _, v := range valueArray(state["problemOrders"]) {
		e := valueMap(v)
		if e["problemId"] == request.ProblemID {
			entry = e
			break
		}
	}
	if entry == nil {
		return nil, "", false, stockConflict("Проблема не найдена.")
	}
	order := valueMap(entry["order"])
	p := valueMap(entry["problem"])
	if order["source"] != "ozon" || p["resolutionStatus"] == "returned" || p["closedOutcome"] != nil {
		return nil, "", false, stockConflict("Заказ закрыт или является историческим примером.")
	}
	var oid string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM assembly_order_records WHERE source='ozon' AND order_number=$1`, textValue(order["number"])).Scan(&oid); err != nil {
		return nil, "", false, err
	}
	var existing string
	err := tx.QueryRow(ctx, `SELECT id::text FROM assembly_disassembly_sessions WHERE order_id=$1::uuid`, oid).Scan(&existing)
	if err == nil {
		return encodeValue(state), "Передача уже существует. Продолжите исходную сессию разбора.", true, nil
	}
	if err != pgx.ErrNoRows {
		return nil, "", false, err
	}
	if p["type"] != "cancelled_order" && p["requiresDisassembly"] != true {
		return nil, "", false, stockConflict("Для этой проблемы полный физический разбор не требуется.")
	}
	if sameAssemblyOrder(valueMap(state["activeOrder"]), order) {
		return nil, "", false, stockConflict("Сначала остановите сборку этого заказа в проблемные.")
	}
	if p["type"] == "cancelled_order" {
		id := textValue(p["ozonOperationId"])
		if !assemblyDraftRequestID.MatchString(id) {
			return nil, "", false, stockConflict("Для передачи требуется проверенное подтверждение отмены.")
		}
		op, e := loadOzonOperation(ctx, tx, id)
		if e != nil {
			return nil, "", false, e
		}
		if op.OrderID != oid || op.Outcome != "cancelled" {
			return nil, "", false, stockConflict("Отмена заказа не подтверждена.")
		}
	}
	picks := valueArray(valueMap(entry["assembly"])["picks"])
	if len(picks) == 0 {
		return nil, "", false, stockConflict("За заказом нет сохранённых реальных отборов для разбора.")
	}
	if err = verifyWarehousePicks(ctx, tx, oid, picks); err != nil {
		return nil, "", false, err
	}
	// An unresolved external confirmation cannot be replaced by physically undoing its goods.
	for _, v := range valueArray(state["problemOrders"]) {
		e := valueMap(v)
		other := valueMap(e["problem"])
		if sameAssemblyOrder(valueMap(e["order"]), order) && other["ozonPurpose"] == "confirm_shipment" {
			op, e := loadOzonOperation(ctx, tx, textValue(other["ozonOperationId"]))
			if e != nil {
				return nil, "", false, e
			}
			if op.Outcome != "cancelled" && op.Outcome != "rejected" && op.Outcome != "not_applied" {
				return nil, "", false, stockConflict("Сначала выясните фактический результат подтверждения Ozon. Физический разбор неоднозначной операции запрещён.")
			}
		}
	}
	reason := textValue(p["type"])
	var sid string
	if err = tx.QueryRow(ctx, `INSERT INTO assembly_disassembly_sessions(order_id,problem_id,snapshot) VALUES($1::uuid,$2::uuid,$3::jsonb) RETURNING id::text`, oid, request.ProblemID, []byte(encodeValue(map[string]any{"order": order, "assembly": entry["assembly"], "reason": reason}))).Scan(&sid); err != nil {
		return nil, "", false, err
	}
	for n, v := range picks {
		pick := valueMap(v)
		sku := textValue(pick["sku"])
		returnable := true
		for _, issue := range valueArray(state["problemOrders"]) {
			e := valueMap(issue)
			other := valueMap(e["problem"])
			if !sameAssemblyOrder(valueMap(e["order"]), order) || other["type"] != "damaged_product" || other["resolutionStatus"] == "returned" || other["closedOutcome"] != nil {
				continue
			}
			for _, position := range valueArray(valueMap(other["creationContext"])["affectedPositions"]) {
				if valueMap(position)["sku"] == sku {
					returnable = false
				}
			}
		}
		payload := map[string]any{"sku": sku, "name": findOrderItem(order, sku)["name"], "sourceCell": pick["cell"], "pickId": pick["pickId"], "pickedAt": pick["occurredAt"], "quantity": 1}
		if _, err = tx.Exec(ctx, `INSERT INTO assembly_disassembly_items(session_id,original_operation_id,ordinal,returnable,payload) VALUES($1::uuid,$2::uuid,$3,$4,$5::jsonb)`, sid, textValue(pick["operationId"]), n, returnable, []byte(encodeValue(payload))); err != nil {
			return nil, "", false, err
		}
	}
	// Release only unpicked reserve. This does not put picked goods into cell_stock.
	if _, err = tx.Exec(ctx, `UPDATE assembly_order_reservations SET quantity=0,status='released',updated_at=now() WHERE order_id=$1::uuid`, oid); err != nil {
		return nil, "", false, err
	}
	for _, v := range valueArray(state["problemOrders"]) {
		e := valueMap(v)
		if sameAssemblyOrder(valueMap(e["order"]), order) && valueMap(e["problem"])["resolutionStatus"] != "returned" {
			valueMap(e["problem"])["disassemblySessionId"] = sid
		}
	}
	appendDisassemblyEvent(state, order, timestamp, "disassembly_transfer", "Заказ передан в разбор", fmt.Sprintf("Сессия: %s. Сохранено исходных отборов: %d. Остатки не изменены.", sid, len(picks)))
	return encodeValue(state), "Передача создана. Остатки не изменены; физический возврат выполняется в окне «Разбор».", true, nil
}

func applyDisassembly(ctx context.Context, tx pgx.Tx, current json.RawMessage, request assemblyProblemOperation, timestamp string) (json.RawMessage, string, bool, error) {
	enabled, err := disassemblyEnabled(ctx, tx)
	if err != nil {
		return nil, "", false, err
	}
	if !enabled {
		return nil, "", false, stockConflict("Примените миграцию 018.")
	}
	state, err := decodeWorkspace(current)
	if err != nil {
		return nil, "", false, err
	}
	if request.Action == "transfer-disassembly" {
		return transferDisassembly(ctx, tx, state, request, timestamp)
	}
	returnsRelation, err := disassemblyReturnsRelation(ctx, tx)
	if err != nil {
		return nil, "", false, err
	}
	var status, oid string
	var snapshot json.RawMessage
	var pending *string
	err = tx.QueryRow(ctx, `SELECT status,order_id::text,snapshot,pending_item_id::text FROM assembly_disassembly_sessions WHERE id=$1::uuid FOR UPDATE`, request.SessionID).Scan(&status, &oid, &snapshot, &pending)
	if err == pgx.ErrNoRows {
		return nil, "", false, stockConflict("Сессия разбора не найдена.")
	}
	if err != nil {
		return nil, "", false, err
	}
	order := valueMap(valueMapFromRaw(snapshot)["order"])
	if status == "completed" {
		return nil, "", false, stockConflict("Разбор уже завершён.")
	}
	if request.Action == "disassembly-start" {
		if status == "active" {
			return encodeValue(state), "Продолжайте сохранённый разбор.", true, nil
		}
		var busy bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assembly_disassembly_sessions WHERE status='active' AND id<>$1::uuid)`, request.SessionID).Scan(&busy); err != nil {
			return nil, "", false, err
		}
		if busy {
			return nil, "", false, stockConflict("Сначала завершите активный разбор.")
		}
		if _, err = tx.Exec(ctx, `UPDATE assembly_disassembly_sessions SET status='active',started_at=COALESCE(started_at,clock_timestamp()),log=CASE WHEN pending_item_id IS NULL THEN 'Сканируйте товар для возврата.' ELSE 'Товар уже подтверждён. Сканируйте исходную ячейку.' END,error=false WHERE id=$1::uuid`, request.SessionID); err != nil {
			return nil, "", false, err
		}
		appendDisassemblyEvent(state, order, timestamp, "disassembly_start", "Разбор начат", "Запуск не изменяет складские остатки.")
		if pending != nil {
			return encodeValue(state), "Разбор продолжен. Товар уже подтверждён; сканируйте исходную ячейку.", true, nil
		}
		return encodeValue(state), "Разбор начат. Сканируйте товар.", true, nil
	}
	if status != "active" {
		return nil, "", false, stockConflict("Сначала начните разбор.")
	}
	if request.Action == "disassembly-undo-return" {
		if pending != nil {
			return nil, "", false, stockConflict("Сначала завершите текущий цикл возврата товара в ячейку.")
		}
		if returnsRelation != "assembly_disassembly_effective_returns" {
			return nil, "", false, stockConflict("Для отмены возврата примените миграцию 019.")
		}
		return undoDisassemblyReturn(ctx, tx, state, order, request, timestamp)
	}
	if request.Action == "disassembly-pause" {
		if _, err = tx.Exec(ctx, `UPDATE assembly_disassembly_sessions SET status='waiting',log='Разбор отложен. Прогресс и подтверждённый товар сохранены.',error=false WHERE id=$1::uuid`, request.SessionID); err != nil {
			return nil, "", false, err
		}
		appendDisassemblyEvent(state, order, timestamp, "disassembly_pause", "Разбор отложен", "Прогресс, исходные отборы и ожидаемая ячейка сохранены. Остатки не изменены.")
		return encodeValue(state), "Разбор отложен. Сохранённое состояние восстановится при продолжении.", true, nil
	}
	if request.Action == "disassembly-finish" {
		var left int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM assembly_disassembly_items i WHERE session_id=$1::uuid AND NOT EXISTS(SELECT 1 FROM `+returnsRelation+` r WHERE r.item_id=i.id)`, request.SessionID).Scan(&left); err != nil {
			return nil, "", false, err
		}
		if left > 0 || pending != nil {
			return nil, "", false, stockConflict("Сначала физически верните все исходные отборы.")
		}
		if err = verifyWarehousePicks(ctx, tx, oid, []any{}); err != nil {
			return nil, "", false, err
		}
		var reserved int
		if err = tx.QueryRow(ctx, `SELECT COALESCE(sum(quantity),0)::int FROM assembly_order_reservations WHERE order_id=$1::uuid AND status='held'`, oid).Scan(&reserved); err != nil {
			return nil, "", false, err
		}
		if reserved != 0 {
			return nil, "", false, stockConflict("У заказа остаётся неотобранный резерв.")
		}
		appendDisassemblyEvent(state, order, timestamp, "disassembly_finish", "Разбор завершён", "Все исходные единицы физически возвращены. Незавершённых движений и резервов нет.")
		for _, v := range valueArray(state["problemOrders"]) {
			e := valueMap(v)
			p := valueMap(e["problem"])
			if sameAssemblyOrder(valueMap(e["order"]), order) && p["resolutionStatus"] != "returned" && p["closedOutcome"] == nil {
				p["closedOutcome"] = "disassembled"
				p["requiresDisassembly"] = false
				if p["type"] == "cancelled_order" {
					p["resolutionStatus"] = "resolved"
				}
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE assembly_disassembly_sessions SET status='completed',completed_at=clock_timestamp(),log='Разбор завершён.',error=false WHERE id=$1::uuid`, request.SessionID); err != nil {
			return nil, "", false, err
		}
		return encodeValue(state), "Заказ разобран. История и исходные отборы сохранены.", true, nil
	}
	fail := func(message string) (json.RawMessage, string, bool, error) {
		_, e := tx.Exec(ctx, `UPDATE assembly_disassembly_sessions SET last_scan=$2,log=$3,error=true WHERE id=$1::uuid`, request.SessionID, request.Code, message)
		return encodeValue(state), message, false, e
	}
	if request.Action != "disassembly-scan" {
		return nil, "", false, stockConflict("Неизвестное действие разбора.")
	}
	if pending == nil {
		var item, sku string
		var eligible bool
		err = tx.QueryRow(ctx, `SELECT i.id::text,i.payload->>'sku',i.returnable FROM assembly_disassembly_items i JOIN assembly_warehouse_operations original ON original.id=i.original_operation_id JOIN products p ON p.id=original.product_id WHERE i.session_id=$1::uuid AND upper(p.sku)=upper($2) AND NOT EXISTS(SELECT 1 FROM `+returnsRelation+` r WHERE r.item_id=i.id) ORDER BY i.ordinal LIMIT 1`, request.SessionID, request.Code).Scan(&item, &sku, &eligible)
		if err == pgx.ErrNoRows {
			return fail("Товар не был отобран для этого заказа или уже возвращён.")
		}
		if err != nil {
			return nil, "", false, err
		}
		if !eligible {
			return fail("Товар отмечен повреждённым. Возврат в пригодный остаток запрещён; требуется отдельная обработка повреждения.")
		}
		if _, err = tx.Exec(ctx, `UPDATE assembly_disassembly_sessions SET pending_item_id=$2::uuid,last_scan=$3,log='Товар подтверждён. Сканируйте исходную ячейку.',error=false WHERE id=$1::uuid`, request.SessionID, item, request.Code); err != nil {
			return nil, "", false, err
		}
		return encodeValue(state), "Товар подтверждён. Ожидается исходная ячейка; остаток ещё не изменён.", true, nil
	}
	var pid, cellID int64
	var cell, sku, operation string
	var eligible bool
	if err = tx.QueryRow(ctx, `SELECT original.product_id,original.cell_id,c.code,i.payload->>'sku',original.id::text,i.returnable FROM assembly_disassembly_items i JOIN assembly_warehouse_operations original ON original.id=i.original_operation_id JOIN cells c ON c.id=original.cell_id WHERE i.id=$1::uuid AND i.session_id=$2::uuid`, *pending, request.SessionID).Scan(&pid, &cellID, &cell, &sku, &operation, &eligible); err != nil {
		return nil, "", false, err
	}
	var archived bool
	if err = tx.QueryRow(ctx, `SELECT archived FROM products WHERE id=$1 FOR NO KEY UPDATE`, pid).Scan(&archived); err != nil {
		return nil, "", false, err
	}
	if archived || !eligible {
		return fail("Товар архивирован или непригоден. Зачисление в доступный остаток запрещено.")
	}
	if err = tx.QueryRow(ctx, `SELECT code FROM cells WHERE id=$1 FOR SHARE`, cellID).Scan(&cell); err != nil {
		return nil, "", false, err
	}
	matched := request.Code == fmt.Sprintf("CELL:%d", cellID)
	if !matched && request.Code == cell {
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM cells WHERE code=$1`, cell).Scan(&count); err != nil {
			return nil, "", false, err
		}
		matched = count == 1
	}
	if !matched {
		return fail(fmt.Sprintf("Неверная или неоднозначная ячейка. Верните товар в %s (CELL:%d).", cell, cellID))
	}
	var damaged bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assembly_problem_records p JOIN assembly_problem_affected_positions a ON a.problem_id=p.id WHERE p.order_id=$1::uuid AND p.problem_type='damaged_product' AND p.disposition='problem' AND p.problem_metadata->>'closedOutcome' IS NULL AND a.sku=$2)`, oid, sku).Scan(&damaged); err != nil {
		return nil, "", false, err
	}
	if damaged {
		return fail("Повреждённый товар нельзя вернуть в пригодный остаток.")
	}
	var categoryModel bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='cells' AND column_name='category_id')`).Scan(&categoryModel); err != nil {
		return nil, "", false, err
	}
	if categoryModel {
		var allowed bool
		if err = tx.QueryRow(ctx, `SELECT c.category_id=p.category_id FROM cells c CROSS JOIN products p WHERE c.id=$1 AND p.id=$2`, cellID, pid).Scan(&allowed); err != nil {
			return nil, "", false, err
		}
		if !allowed {
			return fail("Категория исходной ячейки изменилась. Возврат требует проверки размещения.")
		}
	}
	var returnID string
	if err = tx.QueryRow(ctx, `INSERT INTO assembly_disassembly_returns(request_id,item_id,product_id,cell_id,quantity) VALUES($1::uuid,$2::uuid,$3,$4,1) RETURNING id::text`, request.RequestID, *pending, pid, cellID).Scan(&returnID); err != nil {
		return nil, "", false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO cell_stock(cell_id,product_id,quantity) VALUES($1,$2,1) ON CONFLICT(cell_id,product_id) DO UPDATE SET quantity=cell_stock.quantity+1`, cellID, pid); err != nil {
		return nil, "", false, err
	}
	message := fmt.Sprintf("%s: возвращена 1 шт. в %s [CELL:%d]. Возврат: %s, исходный отбор: %s.", sku, cell, cellID, returnID, operation)
	if _, err = tx.Exec(ctx, `UPDATE assembly_disassembly_sessions SET pending_item_id=NULL,last_scan=$2,log=$3,error=false WHERE id=$1::uuid`, request.SessionID, request.Code, message); err != nil {
		return nil, "", false, err
	}
	appendDisassemblyEvent(state, order, timestamp, "disassembly_return", "Подтверждён физический возврат", message)
	return encodeValue(state), message, true, nil
}
