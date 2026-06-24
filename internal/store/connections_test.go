package store

import "testing"

// A fresh database seeds one "Default" LLM connection from the legacy
// single-endpoint settings and marks it active, so upgraders keep their provider.
func TestSeedLLMConnection(t *testing.T) {
	s := openTemp(t)

	conns, err := s.ListLLMConnections()
	if err != nil {
		t.Fatalf("ListLLMConnections: %v", err)
	}
	if len(conns) != 1 {
		t.Fatalf("expected 1 seeded connection, got %d", len(conns))
	}
	if conns[0].Name != "Default" || conns[0].BaseURL == "" || conns[0].Model == "" {
		t.Fatalf("unexpected seed: %+v", conns[0])
	}

	st, _ := s.GetSettings()
	if st.ActiveLLMConnectionID != conns[0].ID {
		t.Fatalf("active id = %d, want %d", st.ActiveLLMConnectionID, conns[0].ID)
	}
	active, err := s.GetActiveLLMConnection()
	if err != nil || active.ID != conns[0].ID {
		t.Fatalf("GetActiveLLMConnection = %+v (%v)", active, err)
	}
}

func TestLLMConnectionCRUD(t *testing.T) {
	s := openTemp(t)

	c, err := s.SaveLLMConnection(LLMConnection{Name: "OpenAI", BaseURL: "https://api.openai.com/v1", Model: "gpt-4o"})
	if err != nil {
		t.Fatalf("SaveLLMConnection: %v", err)
	}
	if c.ID == 0 || c.UpdatedAt == "" {
		t.Fatalf("expected id+timestamp, got %+v", c)
	}

	c.Model = "gpt-4o-mini"
	if _, err := s.SaveLLMConnection(c); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := s.GetLLMConnection(c.ID)
	if err != nil || got.Model != "gpt-4o-mini" {
		t.Fatalf("update not persisted: %+v (%v)", got, err)
	}

	// Seeded "Default" + the one just added.
	list, _ := s.ListLLMConnections()
	if len(list) != 2 {
		t.Fatalf("expected 2 connections, got %d", len(list))
	}

	if err := s.DeleteLLMConnection(c.ID); err != nil {
		t.Fatalf("DeleteLLMConnection: %v", err)
	}
	list, _ = s.ListLLMConnections()
	if len(list) != 1 {
		t.Fatalf("expected 1 after delete, got %d", len(list))
	}
}

func TestPerConnectionAPIKey(t *testing.T) {
	s := openTemp(t)

	a, _ := s.SaveLLMConnection(LLMConnection{Name: "A", BaseURL: "http://a/v1", Model: "m"})
	b, _ := s.SaveLLMConnection(LLMConnection{Name: "B", BaseURL: "http://b/v1", Model: "m"})

	if k, _ := s.GetConnectionAPIKey(a.ID); k != "" {
		t.Fatalf("expected no key initially, got %q", k)
	}
	if err := s.SetConnectionAPIKey(a.ID, "secret-a"); err != nil {
		t.Fatalf("SetConnectionAPIKey: %v", err)
	}
	if k, _ := s.GetConnectionAPIKey(a.ID); k != "secret-a" {
		t.Fatalf("GetConnectionAPIKey = %q, want secret-a", k)
	}
	// Keys are isolated per connection.
	if k, _ := s.GetConnectionAPIKey(b.ID); k != "" {
		t.Fatalf("connection B leaked A's key: %q", k)
	}

	// HasAPIKey is reflected in the list.
	for _, c := range mustList(t, s) {
		if c.ID == a.ID && !c.HasAPIKey {
			t.Fatalf("A should report HasAPIKey")
		}
		if c.ID == b.ID && c.HasAPIKey {
			t.Fatalf("B should not report HasAPIKey")
		}
	}

	// Clearing removes it.
	if err := s.SetConnectionAPIKey(a.ID, ""); err != nil {
		t.Fatalf("clear key: %v", err)
	}
	if k, _ := s.GetConnectionAPIKey(a.ID); k != "" {
		t.Fatalf("key not cleared: %q", k)
	}
}

func TestDeleteActiveConnectionReassigns(t *testing.T) {
	s := openTemp(t)

	second, _ := s.SaveLLMConnection(LLMConnection{Name: "Second", BaseURL: "http://h/v1", Model: "m"})
	if err := s.SetActiveLLMConnection(second.ID); err != nil {
		t.Fatalf("SetActiveLLMConnection: %v", err)
	}
	if err := s.SetConnectionAPIKey(second.ID, "k"); err != nil {
		t.Fatalf("SetConnectionAPIKey: %v", err)
	}

	if err := s.DeleteLLMConnection(second.ID); err != nil {
		t.Fatalf("DeleteLLMConnection: %v", err)
	}

	// Active must fall back to a remaining connection, and settings must agree.
	active, err := s.GetActiveLLMConnection()
	if err != nil {
		t.Fatalf("GetActiveLLMConnection: %v", err)
	}
	if active.ID == second.ID {
		t.Fatalf("active still points at deleted connection")
	}
	st, _ := s.GetSettings()
	if st.ActiveLLMConnectionID != active.ID {
		t.Fatalf("settings active %d != resolved active %d", st.ActiveLLMConnectionID, active.ID)
	}
	// The deleted connection's key is gone too.
	if k, _ := s.GetConnectionAPIKey(second.ID); k != "" {
		t.Fatalf("deleted connection key lingering: %q", k)
	}
}

func TestActiveFallbackOnStaleID(t *testing.T) {
	s := openTemp(t)

	if err := s.SetActiveLLMConnection(99999); err != nil {
		t.Fatalf("SetActiveLLMConnection: %v", err)
	}
	active, err := s.GetActiveLLMConnection()
	if err != nil {
		t.Fatalf("GetActiveLLMConnection: %v", err)
	}
	if active.ID == 0 || active.ID == 99999 {
		t.Fatalf("expected fallback to a real connection, got %+v", active)
	}
}

func TestNewConnectionAutoActivatesWhenNoneSet(t *testing.T) {
	s := openTemp(t)

	// Remove every connection; active should become 0.
	for _, c := range mustList(t, s) {
		if err := s.DeleteLLMConnection(c.ID); err != nil {
			t.Fatalf("DeleteLLMConnection: %v", err)
		}
	}
	if st, _ := s.GetSettings(); st.ActiveLLMConnectionID != 0 {
		t.Fatalf("expected active=0 after deleting all, got %d", st.ActiveLLMConnectionID)
	}
	if _, err := s.GetActiveLLMConnection(); err == nil {
		t.Fatalf("expected error when no connections exist")
	}

	// Adding one with nothing active should auto-activate it.
	c, _ := s.SaveLLMConnection(LLMConnection{Name: "Only", BaseURL: "http://h/v1", Model: "m"})
	if st, _ := s.GetSettings(); st.ActiveLLMConnectionID != c.ID {
		t.Fatalf("new connection did not auto-activate: active=%d, want %d", 0, c.ID)
	}
}

func mustList(t *testing.T, s *Store) []LLMConnection {
	t.Helper()
	list, err := s.ListLLMConnections()
	if err != nil {
		t.Fatalf("ListLLMConnections: %v", err)
	}
	return list
}
