package crypto

import (
	"crypto/aes"
	"fmt"
)

func maskBrokerPrefix(prefix *[brokerPrefixSize]byte) {
	for index := 1; index < len(prefix); index++ {
		prefix[index] ^= ^(prefix[index-1] << 1)
	}
}

func unmaskBrokerPrefix(prefix *[brokerPrefixSize]byte) {
	for index := len(prefix) - 1; index >= 1; index-- {
		prefix[index] ^= ^(prefix[index-1] << 1)
	}
}

func brokerBodyKey(prefix [brokerPrefixSize]byte) [aes.BlockSize]byte {
	masked := prefix
	maskBrokerPrefix(&masked)
	var result [aes.BlockSize]byte
	copy(result[:brokerPrefixSize], masked[:])
	copy(result[brokerPrefixSize:], prefix[:])
	return result
}

func transformBrokerBody(body []byte, prefix [brokerPrefixSize]byte, inverse bool) ([]byte, error) {
	if len(body)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("broker body length %d is not a multiple of %d", len(body), aes.BlockSize)
	}
	key := brokerBodyKey(prefix)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	result := make([]byte, len(body))
	for offset := 0; offset < len(body); offset += aes.BlockSize {
		if inverse {
			block.Decrypt(result[offset:offset+aes.BlockSize], body[offset:offset+aes.BlockSize])
		} else {
			block.Encrypt(result[offset:offset+aes.BlockSize], body[offset:offset+aes.BlockSize])
		}
	}
	return result, nil
}
