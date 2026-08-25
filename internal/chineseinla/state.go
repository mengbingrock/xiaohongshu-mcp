package chineseinla

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type PreparedState struct {
	DraftID      string    `json:"draft_id,omitempty"`
	ForumID      int       `json:"forum_id"`
	ForumName    string    `json:"forum_name"`
	FormURL      string    `json:"form_url"`
	TargetID     string    `json:"target_id,omitempty"`
	Title        string    `json:"title"`
	Headless     bool      `json:"headless,omitempty"`
	PreviewImage string    `json:"preview_image,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type StateStore struct {
	Path string
}

func (s StateStore) Save(state PreparedState) error {
	if s.Path == "" {
		return errors.New("state path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode prepared state: %w", err)
	}
	temporary := s.Path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write prepared state: %w", err)
	}
	if err := os.Rename(temporary, s.Path); err != nil {
		return fmt.Errorf("commit prepared state: %w", err)
	}
	return nil
}

func (s StateStore) Load() (PreparedState, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PreparedState{}, errors.New("no prepared post found; run prepare first")
		}
		return PreparedState{}, fmt.Errorf("read prepared state: %w", err)
	}
	var state PreparedState
	if err := json.Unmarshal(data, &state); err != nil {
		return PreparedState{}, fmt.Errorf("decode prepared state: %w", err)
	}
	if state.FormURL == "" || state.ForumID <= 0 {
		return PreparedState{}, errors.New("prepared state is incomplete; run prepare again")
	}
	return state, nil
}

func (s StateStore) Clear() error {
	err := os.Remove(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
