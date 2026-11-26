package game

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"wtserver-go/internal/protocol"
)

type GMCommand byte

const (
	GMCommandSilenceOn GMCommand = iota + 1
	GMCommandSilenceOff
	GMCommandMuteBan
	GMCommandKick
	GMCommandBan
	GMCommandSearch
)

type GMCommandRequest struct {
	Command   GMCommand
	Target    string
	Duration  time.Duration
	Permanent bool
	Reason    string
}

// ParseGMCommandRequest parses a command byte followed by optional
// byte-counted UTF-16 target and reason strings.
func ParseGMCommandRequest(payload []byte) (GMCommandRequest, error) {
	if len(payload) < 1 {
		return GMCommandRequest{}, fmt.Errorf("GM command payload is empty")
	}
	request := GMCommandRequest{Command: GMCommand(payload[0])}
	if request.Command < GMCommandSilenceOn || request.Command > GMCommandBan {
		return GMCommandRequest{}, fmt.Errorf("unknown GM command %d", request.Command)
	}
	offset := 1
	if offset == len(payload) {
		return request, nil
	}
	var err error
	if request.Target, offset, err = readCountedUTF16(payload, offset); err != nil {
		return GMCommandRequest{}, fmt.Errorf("parse GM command target: %w", err)
	}
	if offset < len(payload) {
		if request.Reason, offset, err = readCountedUTF16(payload, offset); err != nil {
			return GMCommandRequest{}, fmt.Errorf("parse GM command reason: %w", err)
		}
	}
	if offset != len(payload) {
		return GMCommandRequest{}, fmt.Errorf("GM command has %d trailing bytes", len(payload)-offset)
	}
	request.Target = strings.TrimSpace(request.Target)
	request.Reason = strings.TrimSpace(request.Reason)
	if request.Command == GMCommandMuteBan || request.Command == GMCommandBan {
		arguments := strings.Fields(request.Reason)
		if len(arguments) == 0 {
			return GMCommandRequest{}, fmt.Errorf("%s", moderationUsage(request.Command))
		}
		if request.Duration, request.Permanent, err = parseModerationDuration(
			arguments[0], request.Command == GMCommandBan,
		); err != nil {
			return GMCommandRequest{}, err
		}
		request.Reason = strings.Join(arguments[1:], " ")
	}
	return request, nil
}

func readCountedUTF16(payload []byte, offset int) (string, int, error) {
	if offset >= len(payload) {
		return "", offset, fmt.Errorf("missing UTF-16 byte count")
	}
	length := int(payload[offset])
	offset++
	if length%2 != 0 || offset+length > len(payload) {
		return "", offset, fmt.Errorf("invalid UTF-16 byte count %d", length)
	}
	return decodeUTF16Low(payload[offset : offset+length]), offset + length, nil
}

func ParseGMChatCommand(text string) (GMCommandRequest, bool, error) {
	cleaned := strings.TrimSpace(text)
	switch {
	case strings.EqualFold(cleaned, "/silence on"):
		return GMCommandRequest{Command: GMCommandSilenceOn}, true, nil
	case strings.EqualFold(cleaned, "/silence off"):
		return GMCommandRequest{Command: GMCommandSilenceOff}, true, nil
	}

	for _, candidate := range []struct {
		name    string
		command GMCommand
	}{
		{name: "/muteban", command: GMCommandMuteBan},
		{name: "/kick", command: GMCommandKick},
		{name: "/ban", command: GMCommandBan},
		{name: "/search", command: GMCommandSearch},
	} {
		if strings.EqualFold(cleaned, candidate.name) {
			return GMCommandRequest{Command: candidate.command}, true,
				fmt.Errorf("%s", moderationUsage(candidate.command))
		}
		if len(cleaned) <= len(candidate.name) ||
			!strings.EqualFold(cleaned[:len(candidate.name)], candidate.name) ||
			cleaned[len(candidate.name)] != ' ' {
			continue
		}
		arguments := strings.Fields(cleaned[len(candidate.name):])
		if len(arguments) == 0 {
			return GMCommandRequest{Command: candidate.command}, true,
				fmt.Errorf("%s", moderationUsage(candidate.command))
		}
		request := GMCommandRequest{Command: candidate.command, Target: arguments[0]}
		switch candidate.command {
		case GMCommandMuteBan, GMCommandBan:
			if len(arguments) < 2 {
				return request, true, fmt.Errorf("%s", moderationUsage(candidate.command))
			}
			duration, permanent, err := parseModerationDuration(
				arguments[1], candidate.command == GMCommandBan,
			)
			if err != nil {
				return request, true, err
			}
			request.Duration, request.Permanent = duration, permanent
			if len(arguments) > 2 {
				request.Reason = strings.Join(arguments[2:], " ")
			}
		case GMCommandKick:
			if len(arguments) > 1 {
				request.Reason = strings.Join(arguments[1:], " ")
			}
		case GMCommandSearch:
			if len(arguments) > 1 {
				return request, true, fmt.Errorf("%s", moderationUsage(candidate.command))
			}
		}
		return request, true, nil
	}
	return GMCommandRequest{}, false, nil
}

func parseModerationDuration(value string, allowPermanent bool) (time.Duration, bool, error) {
	value = strings.TrimSpace(value)
	if allowPermanent && (strings.EqualFold(value, "permanent") ||
		strings.EqualFold(value, "perm") || strings.EqualFold(value, "forever")) {
		return 0, true, nil
	}
	if len(value) < 2 {
		return 0, false, fmt.Errorf("invalid duration %q; use values such as 30m, 2h, or 7d", value)
	}
	multiplier, ok := map[byte]time.Duration{
		's': time.Second,
		'm': time.Minute,
		'h': time.Hour,
		'd': 24 * time.Hour,
		'w': 7 * 24 * time.Hour,
	}[value[len(value)-1]|0x20]
	if !ok {
		return 0, false, fmt.Errorf("invalid duration %q; use s, m, h, d, or w", value)
	}
	quantity, err := strconv.ParseUint(value[:len(value)-1], 10, 64)
	if err != nil || quantity == 0 || quantity > uint64(math.MaxInt64/int64(multiplier)) {
		return 0, false, fmt.Errorf("invalid duration %q", value)
	}
	return time.Duration(quantity) * multiplier, false, nil
}

func moderationUsage(command GMCommand) string {
	switch command {
	case GMCommandMuteBan:
		return "Usage: /muteban <callsign> <duration> [reason]"
	case GMCommandKick:
		return "Usage: /kick <callsign> [reason]"
	case GMCommandBan:
		return "Usage: /ban <callsign> <duration|permanent> [reason]"
	case GMCommandSearch:
		return "Usage: /search <callsign>"
	default:
		return "Invalid GM command."
	}
}

func GMCommandAckPayload(command GMCommand, text, subject string) ([]byte, error) {
	if command < GMCommandSilenceOn || command > GMCommandBan {
		return nil, fmt.Errorf("unknown GM command %d", command)
	}
	textUnits := utf16.Encode([]rune(text))
	subjectUnits := utf16.Encode([]rune(subject))
	if len(textUnits)*2 > 255 || len(subjectUnits)*2 > 255 {
		return nil, fmt.Errorf("GM command text exceeds one-byte UTF-16 limit")
	}
	return protocol.NewWriter(4 + len(textUnits)*2 + len(subjectUnits)*2).
		U8(0).
		U8(byte(command)).
		U8(byte(len(textUnits) * 2)).UTF16LE(text).
		U8(byte(len(subjectUnits) * 2)).UTF16LE(subject).
		Clone(), nil
}

func MuteBanPayload(active bool, startedAt, expiresAt time.Time, reason string) ([]byte, error) {
	writer := protocol.NewWriter(64).U8(0)
	if err := writeMuteBanFields(writer, active, startedAt, expiresAt, reason); err != nil {
		return nil, err
	}
	return writer.Clone(), nil
}

func writeMuteBanFields(
	writer *protocol.Writer,
	active bool,
	startedAt, expiresAt time.Time,
	reason string,
) error {
	writer.Bool(active)
	return writeMuteBanMetadata(writer, active, startedAt, expiresAt, reason)
}

func writeMuteBanMetadata(
	writer *protocol.Writer,
	active bool,
	startedAt, expiresAt time.Time,
	reason string,
) error {
	startText, endText := "", ""
	if active {
		if !startedAt.IsZero() {
			startText = startedAt.UTC().Format("200601021504")
		}
		if expiresAt.IsZero() {
			endText = "permanent"
		} else {
			endText = expiresAt.UTC().Format("200601021504")
		}
	} else {
		reason = ""
	}
	reasonUnits := utf16.Encode([]rune(reason))
	if len(startText)+1 > 255 || len(endText)+1 > 255 || len(reasonUnits)*2 > 255 {
		return fmt.Errorf("mute-ban metadata exceeds one-byte length")
	}
	writeMuteBanTime := func(value string) {
		if value == "" {
			writer.U8(0)
			return
		}
		// These ASCII fields include the trailing NUL in their packet count.
		// Twelve timestamp digits plus that NUL fit the 14-byte slots.
		writer.U8(byte(len(value) + 1)).ASCII(value).U8(0)
	}
	writeMuteBanTime(startText)
	writeMuteBanTime(endText)
	writer.U8(byte(len(reasonUnits) * 2)).UTF16LE(reason)
	return nil
}
