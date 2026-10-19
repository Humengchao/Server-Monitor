package models

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"server-monitor/internal/testdb"
)

func TestAIAuthorityReadsRespectCancellationWhileDatabasePoolIsBusy(t *testing.T) {
	raw := testdb.Open(t)
	user, server, _ := testdb.Seed(t, raw)
	db := &DB{Raw: raw, EncryptionKey: "0123456789abcdef0123456789abcdef"}
	raw.SetMaxOpenConns(1)
	for _, kind := range []string{"server", "credential"} {
		t.Run(kind, func(t *testing.T) {
			connection, err := raw.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				var err error
				if kind == "server" {
					_, err = GetServerByIDAndUserContext(ctx, db, server, user)
				} else {
					_, err = GetCredentialByIDContext(ctx, db, uuid.New(), user)
				}
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				connection.Close()
				<-done
				t.Fatal("authorization query ignored cancellation")
			}
		})
	}
}
