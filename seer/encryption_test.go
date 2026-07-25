package seer

import (
	"bytes"
	"strconv"
	"testing"
)

const testPassword = "4G*vk90!Seer-foundation"

func TestEncryptRejectsWeakPasswords(t *testing.T) {
	plaintext := []byte("operator config")
	for _, password := range []string{"password1", "4G*vk90"} {
		if _, err := encrypt(plaintext, password); err == nil {
			t.Errorf("encrypt() accepted weak password %q", password)
		}
	}
}

func TestDecryptRejectsMalformedCiphertext(t *testing.T) {
	for _, ciphertext := range [][]byte{nil, []byte("not base64"), []byte("AA==")} {
		if _, err := decrypt(ciphertext, testPassword); err == nil {
			t.Errorf("decrypt(%q) accepted malformed ciphertext", ciphertext)
		}
	}
}

func TestEncryptRoundTrip(t *testing.T) {
	for _, length := range []int{31, 32, 33} {
		t.Run(strconv.Itoa(length), func(t *testing.T) {
			plaintext := bytes.Repeat([]byte{0x5a}, length)
			ciphertext, err := encrypt(plaintext, testPassword)
			if err != nil {
				t.Fatalf("encrypt() error = %v", err)
			}
			decrypted, err := decrypt(ciphertext, testPassword)
			if err != nil {
				t.Fatalf("decrypt() error = %v", err)
			}
			if !bytes.Equal(plaintext, decrypted) {
				t.Fatalf("round trip length %d produced %d bytes", length, len(decrypted))
			}
		})
	}
}
