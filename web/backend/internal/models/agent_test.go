package models

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"errors"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
	"server-monitor/internal/agenttransport"
	"server-monitor/internal/testdb"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAgentEnrollmentOneUseRevocationAndOwnership(t *testing.T) {
	db := testdb.Open(t)
	user, id, _ := testdb.Seed(t, db)
	ctx := context.Background()
	if _, err := db.Exec(`UPDATE servers SET connection_mode='agent',host='agent' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, _, err := CreateAgentTicket(ctx, db, id, uuid.New()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("foreign enrollment allowed")
	}
	ticket, expires, err := CreateAgentTicket(ctx, db, id, user)
	if err != nil || expires.Before(time.Now()) {
		t.Fatalf("ticket: %v", err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	hello := agenttransport.Hello{Type: "hello", Protocol: 1, Version: "test", Hostname: "host", OS: "linux", Arch: "amd64", Username: "root", HostKey: string(ssh.MarshalAuthorizedKey(signer.PublicKey())), Capabilities: []string{"exec", "pty", "sftp"}}
	var wg sync.WaitGroup
	var accepted atomic.Int32
	tokens := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			registered, token, err := RegisterAgent(ctx, db, ticket, hello)
			if err == nil {
				if registered != id {
					t.Error("wrong server")
				}
				accepted.Add(1)
				tokens <- token
			} else if !errors.Is(err, ErrAgentTicket) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	close(tokens)
	if accepted.Load() != 1 {
		t.Fatalf("ticket used %d times", accepted.Load())
	}
	token := <-tokens
	authID, hostKey, err := AuthenticateAgent(ctx, db, token)
	if err != nil || authID != id || hostKey != hello.HostKey {
		t.Fatal("registered agent cannot authenticate")
	}
	if pinned, err := GetAgentHostKey(ctx, db, id, user); err != nil || pinned != hello.HostKey {
		t.Fatal("owner could not pin enrolled identity", err)
	}
	if _, err := GetAgentHostKey(ctx, db, id, uuid.New()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("foreign user could read agent identity", err)
	}
	var stored, host string
	if err := db.QueryRow(`SELECT a.token_hash,s.host FROM server_agents a JOIN servers s ON s.id=a.server_id WHERE a.server_id=$1`, id).Scan(&stored, &host); err != nil {
		t.Fatal(err)
	}
	if stored == token || host != "agent" {
		t.Fatal("raw token persisted or an ICMP target address was invented")
	}
	if err := RevokeAgent(ctx, db, id, uuid.New()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("foreign revocation allowed")
	}
	if err := RevokeAgent(ctx, db, id, user); err != nil {
		t.Fatal(err)
	}
	if _, _, err := AuthenticateAgent(ctx, db, token); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("revoked token still authenticated")
	}
	if _, err := GetAgentHostKey(ctx, db, id, user); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("revoked agent retained an approval identity", err)
	}
}
