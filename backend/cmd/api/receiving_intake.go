package main

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// All intake entry points use this operation after locking an active session.
// Corrections, placement and placement undo deliberately do not call it.
// The caller commits both the current quantity and immutable event together;
// idempotent handlers check their request receipt before calling this operation.
func addReceivingSessionQuantity(ctx context.Context, tx pgx.Tx, sessionID, productID int64, quantity int) error {
	_, err := tx.Exec(ctx, `INSERT INTO receiving_session_items(session_id,product_id,quantity) VALUES($1,$2,$3)
		ON CONFLICT(session_id,product_id) DO UPDATE SET quantity=receiving_session_items.quantity+EXCLUDED.quantity`, sessionID, productID, quantity)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO receiving_intake_events(session_id,product_id,quantity) VALUES($1,$2,$3)`, sessionID, productID, quantity)
	return err
}
