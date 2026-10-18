package crypto

import (
	"bytes"
	"testing"
)

func TestBackupEncryptionIndependentAndAuthenticated(t *testing.T) {
	plain := []byte(`{"password":"ssh-secret","version":1}`)
	password := "a backup passphrase"
	a, err := SealBackup(plain, password)
	if err != nil {
		t.Fatal(err)
	}
	b, err := SealBackup(plain, password)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) || bytes.Contains(a, plain) {
		t.Fatal("backup not randomized or leaked plaintext")
	}
	restored, err := OpenBackup(a, password)
	if err != nil || !bytes.Equal(restored, plain) {
		t.Fatalf("roundtrip: %v", err)
	}
	if _, err := OpenBackup(a, "incorrect password"); err != ErrBackupPassword {
		t.Fatal("wrong password accepted")
	}
	tampered := append([]byte(nil), a...)
	tampered[len(tampered)-1] ^= 1
	if _, err := OpenBackup(tampered, password); err != ErrBackupPassword {
		t.Fatal("tampering accepted")
	}
	tampered[8] = 255
	if _, err := OpenBackup(tampered, password); err != ErrBackupPassword {
		t.Fatal("unbounded KDF accepted")
	}
}
