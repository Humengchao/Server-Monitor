package models

import (
	"database/sql"
	"sync"
)

var notificationSignals = struct {
	sync.Mutex
	listeners map[*sql.DB]map[chan struct{}]struct{}
}{listeners: make(map[*sql.DB]map[chan struct{}]struct{})}

// Wake local dispatchers after a committed enqueue. Periodic scans still cover
// retries, restarts, and work enqueued by another backend instance.
func SubscribeNotificationWork(db *sql.DB) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	notificationSignals.Lock()
	if notificationSignals.listeners[db] == nil {
		notificationSignals.listeners[db] = make(map[chan struct{}]struct{})
	}
	notificationSignals.listeners[db][ch] = struct{}{}
	notificationSignals.Unlock()
	return ch, func() {
		notificationSignals.Lock()
		delete(notificationSignals.listeners[db], ch)
		if len(notificationSignals.listeners[db]) == 0 {
			delete(notificationSignals.listeners, db)
		}
		notificationSignals.Unlock()
	}
}
func signalNotificationWork(db *sql.DB) {
	notificationSignals.Lock()
	defer notificationSignals.Unlock()
	for ch := range notificationSignals.listeners[db] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
func commitNotificationWork(tx *sql.Tx, db *sql.DB) error {
	if err := tx.Commit(); err != nil {
		return err
	}
	signalNotificationWork(db)
	return nil
}
