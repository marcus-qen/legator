package llm

import "fmt"

const defaultHistoryTokenBudget = 6000

// trimToTokenBudget returns a slice of messages that fit within the given
// token budget. Token count is estimated as len(content)/4 (no external deps).
//
// Messages are walked newest→oldest; the walk stops when adding the next
// message would exceed the budget. At least the last 2 messages are always
// preserved regardless of budget. When any messages are trimmed a system note
// is prepended to the returned slice.
func trimToTokenBudget(messages []Message, budget int) []Message {
	if len(messages) == 0 {
		return messages
	}

	tokens := 0
	cutoff := len(messages) // index from which we start keeping messages

	for i := len(messages) - 1; i >= 0; i-- {
		est := len(messages[i].Content) / 4
		if tokens+est > budget {
			break
		}
		tokens += est
		cutoff = i
	}

	// Always preserve at least the last 2 messages.
	minStart := len(messages) - 2
	if minStart < 0 {
		minStart = 0
	}
	if cutoff > minStart {
		cutoff = minStart
	}

	if cutoff == 0 {
		return messages
	}

	note := Message{
		Role:    RoleSystem,
		Content: fmt.Sprintf("[Context note: %d earlier messages were omitted to stay within context limits]", cutoff),
	}

	result := make([]Message, 0, len(messages)-cutoff+1)
	result = append(result, note)
	result = append(result, messages[cutoff:]...)
	return result
}
