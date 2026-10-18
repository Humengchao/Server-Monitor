package crypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
)

const MaxBackupSize = 16 << 20
const backupHeaderSize = 8 + 4 + 4 + 1 + 16 + 12

var backupMagic = []byte("SMBK\x00\x00\x00\x01")
var ErrBackupPassword = errors.New("incorrect backup password or damaged backup")

// The fixed, authenticated header bounds attacker-selected KDF work before
// derivation. Backup keys are independent from the instance ENCRYPTION_KEY.
func SealBackup(plain []byte, password string) ([]byte, error) {
	if len(password) < 12 || len(password) > 1024 {
		return nil, fmt.Errorf("backup password must be 12–1024 bytes")
	}
	if len(plain) > MaxBackupSize {
		return nil, fmt.Errorf("backup is too large")
	}
	header := make([]byte, backupHeaderSize)
	copy(header, backupMagic)
	binary.BigEndian.PutUint32(header[8:12], 64*1024)
	binary.BigEndian.PutUint32(header[12:16], 3)
	header[16] = 2
	if _, err := io.ReadFull(rand.Reader, header[17:]); err != nil {
		return nil, err
	}
	aead, err := backupCipher(password, header)
	if err != nil {
		return nil, err
	}
	return aead.Seal(header, header[33:45], plain, header), nil
}

func OpenBackup(data []byte, password string) ([]byte, error) {
	if len(password) > 1024 || len(data) < backupHeaderSize+16 || len(data) > MaxBackupSize+backupHeaderSize+16 {
		return nil, ErrBackupPassword
	}
	header := data[:backupHeaderSize]
	if !bytes.Equal(header[:8], backupMagic) || binary.BigEndian.Uint32(header[8:12]) != 64*1024 || binary.BigEndian.Uint32(header[12:16]) != 3 || header[16] != 2 {
		return nil, ErrBackupPassword
	}
	aead, err := backupCipher(password, header)
	if err != nil {
		return nil, ErrBackupPassword
	}
	result, err := aead.Open(nil, header[33:45], data[backupHeaderSize:], header)
	if err != nil {
		return nil, ErrBackupPassword
	}
	return result, nil
}

func backupCipher(password string, header []byte) (cipher.AEAD, error) {
	key := argon2.IDKey([]byte(password), header[17:33], 3, 64*1024, 2, 32)
	defer func() {
		for i := range key {
			key[i] = 0
		}
	}()
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
