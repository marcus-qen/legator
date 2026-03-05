package llm

import (
	"strings"
	"testing"
)

func TestTrimToTokenBudget_Empty(t *testing.T) {
	result := trimToTokenBudget(nil, defaultHistoryTokenBudget)
	if len(result) != 0 {
		t.Fatalf("expected empty result, got %d messages", len(result))
	}

	result = trimToTokenBudget([]Message{}, defaultHistoryTokenBudget)
	if len(result) != 0 {
		t.Fatalf("expected empty result for empty slice, got %d messages", len(result))
	}
}

func TestTrimToTokenBudget_UnderBudget(t *testing.T) {
	messages := []Message{
		{Role: RoleUser, Content: "hello"},
		{Role: RoleAssistant, Content: "world"},
		{Role: RoleUser, Content: "how are you?"},
	}
	result := trimToTokenBudget(messages, 10000)
	if len(result) != len(messages) {
		t.Fatalf("expected all %d messages kept, got %d", len(messages), len(result))
	}
	// No system note should be prepended when nothing is trimmed.
	for _, m := range result {
		if m.Role == RoleSystem {
			t.Fatal("unexpected system note when all messages fit within budget")
		}
	}
}

func TestTrimToTokenBudget_OverBudget(t *testing.T) {
	// Each message has 100 chars → 25 estimated tokens.
	// Budget = 30: only the last message fits (25 tokens), but we keep at least 2.
	content := strings.Repeat("x", 100)
	messages := []Message{
		{Role: RoleUser, Content: content},
		{Role: RoleAssistant, Content: content},
		{Role: RoleUser, Content: content},
		{Role: RoleAssistant, Content: content},
	}
	result := trimToTokenBudget(messages, 30)

	if len(result) == 0 {
		t.Fatal("expected non-empty result")
	}
	// Trimming occurred → first message must be a system note.
	if result[0].Role != RoleSystem {
		t.Fatalf("expected system note as first message after trim, got role %q", result[0].Role)
	}
	// At least 2 non-system messages must be present.
	nonSystem := 0
	for _, m := range result {
		if m.Role != RoleSystem {
			nonSystem++
		}
	}
	if nonSystem < 2 {
		t.Fatalf("expected at least 2 non-system messages, got %d", nonSystem)
	}
}

func TestTrimToTokenBudget_Minimum2Preserved(t *testing.T) {
	// Budget so small that nothing fits; we must still keep the last 2.
	content := strings.Repeat("x", 1000) // 250 estimated tokens each
	messages := []Message{
		{Role: RoleUser, Content: content},
		{Role: RoleAssistant, Content: content},
		{Role: RoleUser, Content: content},
		{Role: RoleAssistant, Content: content},
	}
	result := trimToTokenBudget(messages, 1)

	nonSystem := 0
	for _, m := range result {
		if m.Role != RoleSystem {
			nonSystem++
		}
	}
	if nonSystem < 2 {
		t.Fatalf("expected at least 2 non-system messages preserved, got %d", nonSystem)
	}
}

func TestTrimToTokenBudget_SystemNotePresent(t *testing.T) {
	// Each message: 400 chars → 100 estimated tokens.
	// Budget = 150: last message fits (100 tokens), second-to-last would push to 200 → break.
	// "Always keep at least 2" forces cutoff=2, trimming the first 2.
	content := strings.Repeat("x", 400)
	messages := []Message{
		{Role: RoleUser, Content: content},
		{Role: RoleAssistant, Content: content},
		{Role: RoleUser, Content: content},
		{Role: RoleAssistant, Content: content},
	}
	result := trimToTokenBudget(messages, 150)

	if len(result) == 0 {
		t.Fatal("expected non-empty result")
	}
	if result[0].Role != RoleSystem {
		t.Fatalf("expected first message to be system note, got role %q", result[0].Role)
	}
	if !strings.Contains(result[0].Content, "omitted") {
		t.Fatalf("expected system note to contain 'omitted', got: %q", result[0].Content)
	}
	// Verify the note mentions a count.
	if !strings.Contains(result[0].Content, "2") {
		t.Fatalf("expected system note to report 2 omitted messages, got: %q", result[0].Content)
	}
}
