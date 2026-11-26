package game

import "wtserver-go/internal/store"

func resolveCharacter(team byte, loadout store.CharacterSelection, owned map[int]struct{}) int {
	fallback := 30006
	selected := loadout.RedCharacter
	faction := characterFactionRed
	if team == 1 {
		fallback = 30005
		selected = loadout.BlueCharacter
		faction = characterFactionBlue
	}
	if selected <= 0 {
		return fallback
	}
	if _, ok := owned[selected]; !ok {
		return fallback
	}
	if !characterHasFaction(selected, faction) {
		return fallback
	}
	return selected
}

func characterOwnership(items []store.Item) map[int]struct{} {
	result := make(map[int]struct{})
	for _, item := range items {
		if item.Type == 1 && selectableCharacterID(item.ID) {
			result[item.ID] = struct{}{}
		}
	}
	return result
}

func resolveCharacterSelection(slots map[int]int, firstKey, secondKey int, current store.CharacterSelection) (store.CharacterSelection, int, int) {
	first, second := slots[firstKey], slots[secondKey]
	result := current
	if characterHasFaction(first, characterFactionRed) && characterHasFaction(second, characterFactionBlue) {
		result.RedCharacter, result.BlueCharacter = first, second
	} else if characterHasFaction(first, characterFactionBlue) && characterHasFaction(second, characterFactionRed) {
		result.RedCharacter, result.BlueCharacter = second, first
	}
	return result, first, second
}

func validCharacterPair(first, second int) bool {
	return characterHasFaction(first, characterFactionRed) && characterHasFaction(second, characterFactionBlue) ||
		characterHasFaction(first, characterFactionBlue) && characterHasFaction(second, characterFactionRed)
}

func automaticBasicEcho(initial bool, first, second int) bool {
	if !initial {
		return false
	}
	return first == 30006 && second == 30005 || first == 30005 && second == 30006
}
