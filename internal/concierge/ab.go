package concierge

import "fmt"

func MemoryScopeKeyLessons(channel, userExternalID string) string {
	if userExternalID == "" {
		return "concierge.lessons:global"
	}
	return fmt.Sprintf("concierge.lessons:%s:%s", channel, userExternalID)
}
