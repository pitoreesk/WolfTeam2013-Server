package game

import (
	"time"

	"wtserver-go/internal/protocol"
)

const unlimitedExpiryText = "20991231235959"

// expiryStamp is the inventory protocol's fourteen-digit UTC-like timestamp.
// Invalid or non-expiring application values map to the protocol's unlimited
// marker at this boundary.
type expiryStamp string

func expiryStampFrom(value string) expiryStamp {
	if hasFourteenDigits(value) {
		return expiryStamp(value)
	}

	var compact [14]byte
	length := 0
	for index := 0; index < len(value); index++ {
		current := value[index]
		switch current {
		case ' ', '-', ':':
			continue
		}
		if current < '0' || current > '9' || length == len(compact) {
			return expiryStamp(unlimitedExpiryText)
		}
		compact[length] = current
		length++
	}
	if length != len(compact) {
		return expiryStamp(unlimitedExpiryText)
	}
	return expiryStamp(compact[:])
}

func hasFourteenDigits(value string) bool {
	if len(value) != 14 {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func (stamp expiryStamp) unlimited() bool {
	return string(stamp) == unlimitedExpiryText
}

func (stamp expiryStamp) weaponUnits(quantity int, now time.Time) uint32 {
	if stamp.unlimited() {
		return uint32(max(1, quantity))
	}
	until, err := time.ParseInLocation("20060102150405", string(stamp), time.Local)
	if err != nil {
		return uint32(max(1, quantity))
	}
	remaining := until.Sub(now)
	if remaining <= 0 {
		return 0
	}
	days := (remaining + 24*time.Hour - 1) / (24 * time.Hour)
	const maximum = uint32(0x7fffffff)
	if days > time.Duration(maximum) {
		return maximum
	}
	return uint32(days)
}

func (stamp expiryStamp) characterUnits(now time.Time) uint16 {
	if stamp.unlimited() {
		return 0xffff
	}
	until, err := time.ParseInLocation("20060102150405", string(stamp), time.UTC)
	if err != nil {
		return 0xffff
	}
	remaining := until.Sub(now.UTC())
	if remaining <= 0 {
		return 0
	}
	days := (remaining + 24*time.Hour - 1) / (24 * time.Hour)
	if days > 0xfffe {
		return 0xfffe
	}
	return uint16(days)
}

type inventoryWireLifetime struct {
	ExpiryText string
	Count      uint16
	State      byte
	Flag       byte
}

func (lifetime inventoryWireLifetime) appendTo(writer *protocol.Writer) {
	writer.U8(byte(len(lifetime.ExpiryText))).
		ASCII(lifetime.ExpiryText).
		U16(lifetime.Count).
		U8(lifetime.State).
		U8(lifetime.Flag)
}
