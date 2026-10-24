package data

import (
	"slices"

	"github.com/stubbedev/lys-wm/internal/server"
)

type sourcePhase uint8

const (
	sourceFresh sourcePhase = iota
	sourceUsed
	sourceGone
)

type source struct {
	m      *Manager
	client *server.Client
	mimes  []string
	phase  sourcePhase
	send   func(mime string, fd int) error
	cancel func() error
}

func (s *source) Client() *server.Client {
	return s.client
}

func (s *source) Mimes() []string {
	return s.mimes
}

func (s *source) Send(mime string, fd int) error {
	if s.phase == sourceGone {
		return nil
	}
	return s.send(mime, fd)
}

func (s *source) Cancel() error {
	if s.phase == sourceGone {
		return nil
	}
	return s.cancel()
}

func (s *source) addMime(mime string) {
	if !slices.Contains(s.mimes, mime) {
		s.mimes = append(s.mimes, mime)
	}
}

func (s *source) fresh() bool {
	return s.phase == sourceFresh
}

func (s *source) use() {
	s.phase = sourceUsed
}

func (s *source) end() {
	s.phase = sourceGone
}
