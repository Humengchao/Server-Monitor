package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"server-monitor/internal/models"
	"strconv"
	"sync"
	"time"
)

func alertNotification(rule models.AlertRule, snap models.AlertSnapshot, status string, value float64, at time.Time) *models.AlertNotification {
	message := FormatAlertMessage(rule, snap, value)
	if status == "resolved" {
		message = FormatRecoveryMessage(rule, snap, value)
	}
	payload, err := json.Marshal(webhookPayload{Status: status, Rule: rule.Name, Server: snap.ServerName, Metric: rule.Metric,
		Value: value, Threshold: rule.Threshold, Comparator: rule.Comparator, Message: message, Timestamp: at})
	if err != nil {
		return nil
	}
	return &models.AlertNotification{URL: rule.WebhookURL, Payload: payload, Transition: status}
}

type WebhookDispatcher struct {
	db       *sql.DB
	notifier *WebhookNotifier
	cancel   context.CancelFunc
	done     chan struct{}
}

func StartWebhookDispatcher(db *sql.DB, notifier *WebhookNotifier) *WebhookDispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	d := &WebhookDispatcher{db: db, notifier: notifier, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(d.done)
		wake, unsubscribe := models.SubscribeNotificationWork(db)
		defer unsubscribe()
		runIdleQueue(ctx, wake, d.deliver)
	}()
	return d
}

func (d *WebhookDispatcher) Stop() { d.cancel(); <-d.done }

func webhookRetryDelay(attempt int) time.Duration {
	return exponentialDelay(attempt, 5*time.Second, 15*time.Minute)
}

func (d *WebhookDispatcher) deliver(ctx context.Context) bool {
	queryContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	items, err := models.ClaimAlertWebhooks(queryContext, d.db, 4)
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("alerts: claim webhook deliveries: %v", err)
		}
		return false
	}
	var pending sync.WaitGroup
	for _, item := range items {
		pending.Add(1)
		go func(item models.PendingWebhook) {
			defer pending.Done()
			err := d.notifier.postWithID(ctx, item.Target, item.Payload, strconv.FormatInt(item.ID, 10))
			if err != nil && ctx.Err() == nil {
				log.Printf("alerts: webhook delivery %d attempt %d failed; will retry", item.ID, item.Attempts)
			}
			// Shutdown leaves the lease pending for the next process.
			if ctx.Err() != nil {
				return
			}
			finishContext, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if updateErr := models.FinishAlertWebhook(finishContext, d.db, item, err == nil, webhookRetryDelay(item.Attempts)); updateErr != nil {
				log.Printf("alerts: record webhook delivery %d: %v", item.ID, updateErr)
			}
		}(item)
	}
	pending.Wait()
	return len(items) > 0
}
