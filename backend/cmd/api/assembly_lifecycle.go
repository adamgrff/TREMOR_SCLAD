package main

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

func recordAssemblyStarted(ctx context.Context, tx pgx.Tx, orderID, requestID string) error {
	_, err := tx.Exec(ctx, `INSERT INTO assembly_lifecycle_events(order_id,cycle_id,event_type,request_id)
		VALUES($1::uuid,$2::uuid,'started',$2::uuid)`, orderID, requestID)
	return err
}

func latestAssemblyCycle(ctx context.Context, tx pgx.Tx, orderID string) (string, error) {
	var cycle string
	err := tx.QueryRow(ctx, `SELECT cycle_id::text FROM assembly_lifecycle_events
		WHERE order_id=$1::uuid AND event_type='started' ORDER BY id DESC LIMIT 1`, orderID).Scan(&cycle)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil // A legacy active order has no proven start timestamp.
	}
	return cycle, err
}

// Called only by the existing verified finalization of a finish operation.
// The operation payload freezes the cycle at finish, so a late receipt cannot
// attach to a later start. Legacy completions are recorded without a fake start.
func recordAssemblyCompleted(ctx context.Context, tx pgx.Tx, op ozonJournalOperation) error {
	cycle := textValue(valueMapFromRaw(op.Payload)["lifecycleCycleId"])
	if cycle == "" {
		cycle = op.ID
	}
	_, err := tx.Exec(ctx, `INSERT INTO assembly_lifecycle_events(order_id,cycle_id,event_type,operation_id)
		VALUES($1::uuid,$2::uuid,'completed',$3::uuid) ON CONFLICT(operation_id) DO NOTHING`, op.OrderID, cycle, op.ID)
	return err
}
