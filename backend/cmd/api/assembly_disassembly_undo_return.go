package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Called only for an active, non-pending session, inside saveAssemblyWorkspace's
// version lock/transaction. Its deferred request receipt FK is committed there.
func undoDisassemblyReturn(ctx context.Context, tx pgx.Tx, state, order map[string]any, request assemblyProblemOperation, timestamp string) (json.RawMessage, string, bool, error) {
	var returnID, originalOperation, sku string
	var productID, cellID, originalProduct, originalCell int64
	var quantity int
	err := tx.QueryRow(ctx, `SELECT r.id::text,r.product_id,r.cell_id,r.quantity,
  original.product_id,original.cell_id,original.id::text,i.payload->>'sku'
  FROM assembly_disassembly_effective_returns effective
  JOIN assembly_disassembly_returns r ON r.id=effective.id
  JOIN assembly_disassembly_items i ON i.id=r.item_id
  JOIN assembly_warehouse_operations original ON original.id=i.original_operation_id
  WHERE i.session_id=$1::uuid
  ORDER BY r.occurred_at DESC,r.id DESC LIMIT 1 FOR UPDATE OF r`, request.SessionID).Scan(&returnID, &productID, &cellID, &quantity, &originalProduct, &originalCell, &originalOperation, &sku)
	if err == pgx.ErrNoRows {
		return nil, "", false, stockConflict("Нет действующего физического возврата для отмены.")
	}
	if err != nil {
		return nil, "", false, err
	}
	if quantity != 1 || productID != originalProduct || cellID != originalCell {
		return nil, "", false, stockConflict("Возврат не соответствует исходному товару и ячейке.")
	}
	// Match the product/cell lock order used by the existing warehouse writers.
	var lockedProduct int64
	if err = tx.QueryRow(ctx, `SELECT id FROM products WHERE id=$1 FOR NO KEY UPDATE`, productID).Scan(&lockedProduct); err != nil {
		return nil, "", false, err
	}
	var cell string
	if err = tx.QueryRow(ctx, `SELECT code FROM cells WHERE id=$1 FOR SHARE`, cellID).Scan(&cell); err != nil {
		return nil, "", false, err
	}
	result, err := tx.Exec(ctx, `UPDATE cell_stock SET quantity=quantity-1 WHERE cell_id=$1 AND product_id=$2 AND quantity>0`, cellID, productID)
	if err != nil {
		return nil, "", false, err
	}
	if result.RowsAffected() != 1 {
		return nil, "", false, stockConflict(fmt.Sprintf("Недостаточно товара в исходной ячейке %s [CELL:%d] для отмены возврата.", cell, cellID))
	}
	var reversalID string
	if err = tx.QueryRow(ctx, `INSERT INTO assembly_disassembly_return_reversals(request_id,return_id) VALUES($1::uuid,$2::uuid) RETURNING id::text`, request.RequestID, returnID).Scan(&reversalID); err != nil {
		return nil, "", false, err
	}
	message := fmt.Sprintf("%s: отменён возврат 1 шт. из %s [CELL:%d]. Возврат: %s, компенсация: %s, исходный отбор: %s. Сканируйте товар.", sku, cell, cellID, returnID, reversalID, originalOperation)
	appendDisassemblyEvent(state, order, timestamp, "disassembly_return_reversal", "Отменён физический возврат", message)
	if _, err = tx.Exec(ctx, `UPDATE assembly_disassembly_sessions SET log=$2,error=false WHERE id=$1::uuid`, request.SessionID, message); err != nil {
		return nil, "", false, err
	}
	return encodeValue(state), message, true, nil
}
