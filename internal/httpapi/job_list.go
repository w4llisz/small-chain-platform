package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/w4llisz/small-chain-platform/internal/jobs"
	"github.com/w4llisz/small-chain-platform/internal/postgres"
)

const maxJobCursorLength = 512

type jobListCursor struct {
	Version   int        `json:"v"`
	State     jobs.State `json:"state,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	ID        string     `json:"id"`
}

type jobListResponse struct {
	Jobs       []postgres.JobSummary `json:"jobs"`
	NextCursor string                `json:"next_cursor,omitempty"`
}

func encodeJobListCursor(position postgres.JobPosition, state jobs.State) (string, error) {
	encoded, err := json.Marshal(jobListCursor{
		Version:   1,
		State:     state,
		CreatedAt: position.CreatedAt.UTC(),
		ID:        position.ID,
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeJobListCursor(raw string, state jobs.State) (*postgres.JobPosition, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > maxJobCursorLength {
		return nil, errors.New("cursor is too long")
	}
	encoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, errors.New("cursor is not valid base64url")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var cursor jobListCursor
	if err := decoder.Decode(&cursor); err != nil {
		return nil, errors.New("cursor payload is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("cursor payload has trailing data")
	}
	if cursor.Version != 1 || cursor.State != state || cursor.CreatedAt.IsZero() || cursor.ID == "" {
		return nil, errors.New("cursor does not match this query")
	}
	return &postgres.JobPosition{CreatedAt: cursor.CreatedAt.UTC(), ID: cursor.ID}, nil
}

func validJobListState(state jobs.State) bool {
	switch state {
	case "", jobs.Queued, jobs.Running, jobs.Retrying, jobs.Succeeded, jobs.Failed, jobs.Canceled:
		return true
	default:
		return false
	}
}
