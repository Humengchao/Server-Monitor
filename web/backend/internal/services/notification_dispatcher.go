package services

import (
	"context"
	"log"
	"strconv"
	"sync"
	"time"

	"server-monitor/internal/models"
)

// NotificationDispatcher delivers a bounded batch from the durable channel
// queue. Leases expire after a crash. A stable ID is sent on every retry;
// delivery is at-least-once because a crash may follow remote acceptance.
type NotificationDispatcher struct {
	db       *models.DB
	sender   *NotificationSender
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
}

func StartNotificationDispatcher(db *models.DB, sender *NotificationSender) *NotificationDispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	d := &NotificationDispatcher{db: db, sender: sender, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(d.done)
		wake, unsubscribe := models.SubscribeNotificationWork(db.Raw)
		defer unsubscribe()
		runIdleQueue(ctx, wake, d.deliver)
	}()
	return d
}
func (d *NotificationDispatcher) Stop() { d.stopOnce.Do(d.cancel); <-d.done }
func (d *NotificationDispatcher) deliver(ctx context.Context) bool {
	query, cancel := context.WithTimeout(ctx, 10*time.Second)
	items, err := models.ClaimNotifications(query, d.db, 4)
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("notifications: queue claim failed")
		}
		return false
	}
	var group sync.WaitGroup
	for _, item := range items {
		group.Add(1)
		go func(item models.PendingNotification) {
			defer group.Done()
			lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
			channel, err := models.GetNotificationChannel(lookup, d.db, item.ChannelID, item.UserID)
			cancel()
			if err == nil && !channel.Enabled {
				return
			}
			if err == nil {
				// The channel may have been edited between claim and config read.
				// Skip invalidated leases before causing an external side effect.
				check, cancel := context.WithTimeout(ctx, 5*time.Second)
				var current bool
				err = d.db.Raw.QueryRowContext(check, `SELECT EXISTS(SELECT 1 FROM notification_deliveries
                WHERE id=$1 AND lease_version=$2 AND status='sending')`, item.ID, item.LeaseVersion).Scan(&current)
				cancel()
				if err == nil && !current {
					return
				}
			}
			if err == nil {
				err = d.sender.Send(ctx, channel, item.Payload, strconv.FormatInt(item.ID, 10))
			}
			if ctx.Err() != nil {
				return
			}
			finish, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if updateErr := models.FinishNotification(finish, d.db, item, err == nil, SafeNotificationError(err), webhookRetryDelay(item.Attempts)); updateErr != nil {
				log.Printf("notifications: could not record delivery %d", item.ID)
			}
		}(item)
	}
	group.Wait()
	return len(items) > 0
}
