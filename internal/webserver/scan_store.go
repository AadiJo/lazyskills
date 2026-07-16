package webserver

import (
	"crypto/sha256"
	"encoding/json"
	"sync"

	"github.com/alvinunreal/lazyskills/internal/model"
)

type scanStore struct {
	mu          sync.Mutex
	cwd         string
	scan        func(string) (model.ScanResult, error)
	latest      model.ScanResult
	fingerprint [32]byte
	generation  uint64
}

func newScanStore(cwd string, scanner func(string) (model.ScanResult, error)) *scanStore {
	return &scanStore{cwd: cwd, scan: scanner}
}

func (s *scanStore) Scan() (model.ScanResult, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, err := s.scan(s.cwd)
	if err != nil {
		return model.ScanResult{}, s.generation, err
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return model.ScanResult{}, s.generation, err
	}
	fingerprint := sha256.Sum256(payload)
	if s.generation == 0 || fingerprint != s.fingerprint {
		s.generation++
		s.fingerprint = fingerprint
	}
	s.latest = snapshot
	return snapshot, s.generation, nil
}
