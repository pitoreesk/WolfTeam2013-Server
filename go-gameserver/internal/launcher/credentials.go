package launcher

import (
	"crypto/aes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
)

const (
	loginRequestHeaderSize = 6
	loginEnvelopeSize      = 32
	loginEnvelopeEnd       = loginRequestHeaderSize + loginEnvelopeSize
	loginChunkDataSize     = 12
	maxLauncherPassword    = 20
)

var (
	errLoginFrame = errors.New("invalid launcher login frame")
	launcherKey   = [aes.BlockSize]byte{
		0xa9, 0x27, 0x53, 0x04, 0x1b, 0xfc, 0xac, 0xe6,
		0x5b, 0x23, 0x38, 0x34, 0x68, 0x46, 0x03, 0x8c,
	}
)

// DecodeCredentials decodes the legacy retail launcher's 0x1310 request.
// The request uses AES-128 ECB and SHA-0, with a four-byte prefix in every
// encrypted password block. SHA-0 is intentional; replacing it with SHA-1
// breaks compatibility with Launcher.exe.
func DecodeCredentials(frame []byte) (string, string, error) {
	if len(frame) < loginEnvelopeEnd+aes.BlockSize || (len(frame)-loginEnvelopeEnd)%aes.BlockSize != 0 {
		return "", "", fmt.Errorf("%w: got %d bytes", errLoginFrame, len(frame))
	}
	declared, ok := FrameSize(frame)
	if !ok || declared != len(frame) {
		return "", "", fmt.Errorf("%w: declared size %d, got %d", errLoginFrame, declared, len(frame))
	}
	messageID, ok := MessageID(frame)
	if !ok || messageID != MessageLoginRequest {
		return "", "", fmt.Errorf("%w: message ID %#x", errLoginFrame, messageID)
	}

	staticCipher, err := aes.NewCipher(launcherKey[:])
	if err != nil {
		return "", "", fmt.Errorf("create launcher envelope cipher: %w", err)
	}
	var envelope [loginEnvelopeSize]byte
	for offset := 0; offset < len(envelope); offset += aes.BlockSize {
		staticCipher.Decrypt(envelope[offset:offset+aes.BlockSize], frame[loginRequestHeaderSize+offset:loginRequestHeaderSize+offset+aes.BlockSize])
	}

	usernameBytes, ok := terminated(envelope[:aes.BlockSize])
	if !ok || len(usernameBytes) == 0 {
		return "", "", fmt.Errorf("%w: invalid username", errLoginFrame)
	}
	random := binary.LittleEndian.Uint32(envelope[aes.BlockSize : aes.BlockSize+4])
	sessionKey := launcherSessionKey(usernameBytes, random)
	sessionCipher, err := aes.NewCipher(sessionKey[:])
	if err != nil {
		return "", "", fmt.Errorf("create launcher session cipher: %w", err)
	}

	plain := make([]byte, 0, ((len(frame)-loginEnvelopeEnd)/aes.BlockSize)*loginChunkDataSize)
	var block [aes.BlockSize]byte
	for offset := loginEnvelopeEnd; offset < len(frame); offset += aes.BlockSize {
		sessionCipher.Decrypt(block[:], frame[offset:offset+aes.BlockSize])
		plain = append(plain, block[4:]...)
	}
	if len(plain) < maxLauncherPassword {
		return "", "", fmt.Errorf("%w: password payload has %d bytes", errLoginFrame, len(plain))
	}
	passwordBytes := plain[:maxLauncherPassword]
	if end := indexByte(passwordBytes, 0); end >= 0 {
		passwordBytes = passwordBytes[:end]
	}
	return string(usernameBytes), string(passwordBytes), nil
}

func launcherSessionKey(username []byte, random uint32) [aes.BlockSize]byte {
	seed := make([]byte, 0, len(username)+8+4)
	seed = append(seed, username...)
	seed = append(seed, "aeria_bc"...)
	seed = binary.LittleEndian.AppendUint32(seed, random)
	digest := sha0(seed)

	// The client passes the in-memory uint32 SHA-0 state directly to its AES
	// key setup. Launcher.exe is 32-bit little-endian, so reverse each digest
	// word relative to the conventional big-endian hash representation.
	var key [aes.BlockSize]byte
	for offset := 0; offset < len(key); offset += 4 {
		value := binary.BigEndian.Uint32(digest[offset : offset+4])
		binary.LittleEndian.PutUint32(key[offset:offset+4], value)
	}
	return key
}

func sha0(message []byte) [20]byte {
	bitLength := uint64(len(message)) * 8
	paddedLength := (len(message) + 9 + 63) &^ 63
	padded := make([]byte, paddedLength)
	copy(padded, message)
	padded[len(message)] = 0x80
	binary.BigEndian.PutUint64(padded[len(padded)-8:], bitLength)

	h := [5]uint32{0x67452301, 0xefcdab89, 0x98badcfe, 0x10325476, 0xc3d2e1f0}
	var words [80]uint32
	for offset := 0; offset < len(padded); offset += 64 {
		for i := range 16 {
			words[i] = binary.BigEndian.Uint32(padded[offset+i*4 : offset+i*4+4])
		}
		for i := 16; i < len(words); i++ {
			// SHA-0 differs from SHA-1 here: the expanded word is not rotated.
			words[i] = words[i-3] ^ words[i-8] ^ words[i-14] ^ words[i-16]
		}
		a, b, c, d, e := h[0], h[1], h[2], h[3], h[4]
		for i, word := range words {
			var f, constant uint32
			switch {
			case i < 20:
				f = (b & c) | (^b & d)
				constant = 0x5a827999
			case i < 40:
				f = b ^ c ^ d
				constant = 0x6ed9eba1
			case i < 60:
				f = (b & c) | (b & d) | (c & d)
				constant = 0x8f1bbcdc
			default:
				f = b ^ c ^ d
				constant = 0xca62c1d6
			}
			temporary := bits.RotateLeft32(a, 5) + f + e + constant + word
			e, d, c, b, a = d, c, bits.RotateLeft32(b, 30), a, temporary
		}
		h[0] += a
		h[1] += b
		h[2] += c
		h[3] += d
		h[4] += e
	}

	var digest [20]byte
	for i, value := range h {
		binary.BigEndian.PutUint32(digest[i*4:i*4+4], value)
	}
	return digest
}

func terminated(value []byte) ([]byte, bool) {
	end := indexByte(value, 0)
	if end < 0 {
		return nil, false
	}
	return value[:end], true
}

func indexByte(value []byte, target byte) int {
	for i, current := range value {
		if current == target {
			return i
		}
	}
	return -1
}
