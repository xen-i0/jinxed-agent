package main

import (
	"crypto/rc4"
	"errors"
)

// rc4Crypt applies the RC4 stream cipher. Encryption and decryption are the
// same operation; the Adaptix framework uses a 16 byte key in both directions
// (listener key for the registration beat, per-session key for task traffic).
func rc4Crypt(data, key []byte) ([]byte, error) {
	if len(key) == 0 {
		return nil, errors.New("empty rc4 key")
	}
	c, err := rc4.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(data))
	c.XORKeyStream(out, data)
	return out, nil
}

// rc4CryptInPlace encrypts/decrypts buf without allocating a second buffer.
func rc4CryptInPlace(buf, key []byte) error {
	if len(key) == 0 {
		return errors.New("empty rc4 key")
	}
	c, err := rc4.NewCipher(key)
	if err != nil {
		return err
	}
	c.XORKeyStream(buf, buf)
	return nil
}
