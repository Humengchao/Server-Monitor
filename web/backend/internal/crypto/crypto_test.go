package crypto

import "testing"

func TestEncryptDecryptPreservesPasswordBytes(t *testing.T) {
	const key = "0123456789abcdef0123456789abcdef"
	password := " \tpassword with whitespace\r\n"

	encrypted, err := Encrypt(password, key)
	if err != nil {
		t.Fatalf("Encrypt() unexpected error: %v", err)
	}
	decrypted, err := Decrypt(encrypted, key)
	if err != nil {
		t.Fatalf("Decrypt() unexpected error: %v", err)
	}
	if decrypted != password {
		t.Fatalf("Decrypt(Encrypt(password)) changed password bytes: got %q, want %q", decrypted, password)
	}
}
