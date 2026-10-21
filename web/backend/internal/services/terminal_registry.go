package services

import (
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

var ErrTerminalAuthorization = errors.New("terminal authorization expired or was revoked")

type registeredTerminal struct {
	version time.Time
	close   func()
}

var terminals = struct {
	sync.Mutex
	cutoffs map[uuid.UUID]time.Time
	users   map[uuid.UUID]map[*registeredTerminal]struct{}
}{cutoffs: make(map[uuid.UUID]time.Time), users: make(map[uuid.UUID]map[*registeredTerminal]struct{})}

// Register before starting a shell. The version is the database session
// version validated by authentication, not the JWT's rounded issue time.
func RegisterTerminalSession(user uuid.UUID, version, expiresAt time.Time, closeSession func()) (func(), error) {
	terminals.Lock()
	if user == uuid.Nil || version.IsZero() || !expiresAt.After(time.Now()) || version.Before(terminals.cutoffs[user]) {
		terminals.Unlock()
		return nil, ErrTerminalAuthorization
	}
	entry := &registeredTerminal{version: version, close: closeSession}
	if terminals.users[user] == nil {
		terminals.users[user] = make(map[*registeredTerminal]struct{})
	}
	terminals.users[user][entry] = struct{}{}
	terminals.Unlock()
	var once sync.Once
	unregister := func() {
		once.Do(func() {
			terminals.Lock()
			delete(terminals.users[user], entry)
			if len(terminals.users[user]) == 0 {
				delete(terminals.users, user)
			}
			terminals.Unlock()
		})
	}
	timer := time.AfterFunc(time.Until(expiresAt), func() { unregister(); closeSession() })
	return func() { timer.Stop(); unregister() }, nil
}

func RevokeUserTerminalSessions(user uuid.UUID, validAfter time.Time) {
	terminals.Lock()
	if validAfter.After(terminals.cutoffs[user]) {
		terminals.cutoffs[user] = validAfter
	}
	var closeSessions []func()
	for entry := range terminals.users[user] {
		if entry.version.Before(validAfter) {
			closeSessions = append(closeSessions, entry.close)
			delete(terminals.users[user], entry)
		}
	}
	if len(terminals.users[user]) == 0 {
		delete(terminals.users, user)
	}
	terminals.Unlock()
	for _, closeSession := range closeSessions {
		closeSession()
	}
}
