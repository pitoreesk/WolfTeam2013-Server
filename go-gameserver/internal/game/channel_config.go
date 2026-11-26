package game

import (
	"context"

	"wtserver-go/internal/store"
)

// configuredChannelType returns the category for the enabled channel served on
// this game port. Backends without channel configuration retain the normal
// channel behavior used by load tests and embedders.
func (s *Server) configuredChannelType(ctx context.Context) store.ChannelType {
	source, ok := s.users.(store.ChannelSource)
	if !ok {
		return store.ChannelTypeNormal
	}
	channels, err := source.Channels(ctx)
	if err != nil {
		s.logger.Warn("channel configuration unavailable; using normal channel type",
			"port", s.matchPort, "error", err)
		return store.ChannelTypeNormal
	}
	configuredType := store.ChannelTypeNormal
	found := false
	for _, channel := range channels {
		if channel.Port != s.matchPort {
			continue
		}
		if !channel.Type.Valid() {
			s.logger.Warn("channel configuration has invalid type; using normal channel type",
				"port", s.matchPort, "type", channel.Type)
			return store.ChannelTypeNormal
		}
		if found && channel.Type != configuredType {
			s.logger.Warn("channels sharing the game port have conflicting types; using normal channel type",
				"port", s.matchPort, "first_type", configuredType, "conflicting_type", channel.Type)
			return store.ChannelTypeNormal
		}
		configuredType = channel.Type
		found = true
	}
	return configuredType
}
