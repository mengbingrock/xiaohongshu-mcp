package chineseinla

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStateStoreRoundTrip(t *testing.T) {
	t.Parallel()

	store := StateStore{Path: filepath.Join(t.TempDir(), "nested", "prepared.json")}
	want := PreparedState{
		DraftID:      "0123456789abcdef0123456789abcdef",
		ForumID:      21,
		ForumName:    "工作求职",
		FormURL:      BaseURL + "/f/page_pppping/mode_newtopic/f_21.html",
		TargetID:     "target-123",
		Title:        "洛杉矶招聘信息",
		Headless:     true,
		PreviewImage: filepath.Join(t.TempDir(), "preview.png"),
		CreatedAt:    time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC),
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != want {
		t.Fatalf("state = %#v, want %#v", got, want)
	}
	if err := store.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("Load succeeded after Clear")
	}
}

func TestPublishRequiresConfirmationBeforeState(t *testing.T) {
	t.Parallel()
	automation := NewAutomation(Config{})
	if _, err := automation.Publish(t.Context(), false); err == nil {
		t.Fatal("Publish without confirmation unexpectedly succeeded")
	}
}

func TestPublishPreparedRejectsMismatchedDraftBeforeBrowser(t *testing.T) {
	t.Parallel()
	statePath := filepath.Join(t.TempDir(), "prepared.json")
	automation := NewAutomation(Config{StatePath: statePath})
	if err := automation.State.Save(PreparedState{
		DraftID:   "current-draft",
		ForumID:   44,
		ForumName: "轻松一刻",
		FormURL:   BaseURL + "/f/page_pppping/mode_newtopic/f_44.html",
		Title:     "安全测试",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := automation.PublishPrepared(t.Context(), "stale-draft", true); err == nil {
		t.Fatal("PublishPrepared accepted a stale draft ID")
	}
}
