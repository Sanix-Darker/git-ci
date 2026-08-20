package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// TransitionWebhookDelivery records the final processing result for a
// previously reserved delivery. Only received deliveries can transition.
func (s *Store) TransitionWebhookDelivery(ctx context.Context, deliveryID string, next WebhookDeliveryStatus, message *string) (WebhookDelivery, error) {
	if err := requireContext(ctx); err != nil {
		return WebhookDelivery{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return WebhookDelivery{}, err
	}
	deliveryID, err = normalizeRequiredText("webhook delivery ID", deliveryID)
	if err != nil {
		return WebhookDelivery{}, err
	}
	if next != WebhookDeliveryAccepted && next != WebhookDeliveryRejected && next != WebhookDeliveryFailed {
		return WebhookDelivery{}, invalidInput("webhook delivery status", "must be accepted, rejected, or failed")
	}
	message, err = normalizeOptionalString("webhook delivery error", message)
	if err != nil {
		return WebhookDelivery{}, err
	}
	now := nowUTC()
	item, err := scanWebhookDelivery(db.QueryRowContext(ctx, `
		UPDATE webhook_deliveries
		SET status = ?, error_message = ?, processed_at = ?, updated_at = ?
		WHERE id = ? AND status = ?
		RETURNING `+webhookDeliveryColumns,
		next, nullableString(message), now.UnixMilli(), now.UnixMilli(), deliveryID, WebhookDeliveryReceived))
	if errors.Is(err, sql.ErrNoRows) {
		var exists int
		if lookupErr := db.QueryRowContext(ctx, `SELECT 1 FROM webhook_deliveries WHERE id = ?`, deliveryID).Scan(&exists); errors.Is(lookupErr, sql.ErrNoRows) {
			return WebhookDelivery{}, &ErrNotFound{Resource: "webhook delivery", Key: deliveryID}
		}
		return WebhookDelivery{}, &ErrConflict{Resource: "webhook delivery", Field: "status", Value: deliveryID}
	}
	if err != nil {
		return WebhookDelivery{}, fmt.Errorf("store: transition webhook delivery: %w", err)
	}
	return item, nil
}
