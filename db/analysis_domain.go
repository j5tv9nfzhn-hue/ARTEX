package db

import (
	"database/sql"
	"fmt"
	"strings"
)

const (
	AnalysisDomainWebSecurity = "web_security"
	AnalysisDomainSoftwareRE  = "software_re"
)

// NormalizeAnalysisDomain keeps old clients on their existing Web workflow.
func NormalizeAnalysisDomain(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "", AnalysisDomainWebSecurity:
		return AnalysisDomainWebSecurity, nil
	case AnalysisDomainSoftwareRE:
		return AnalysisDomainSoftwareRE, nil
	default:
		return "", fmt.Errorf("invalid analysis_domain: %q", value)
	}
}

// AnalysisDomain reads the immutable task profile through the exploration.
// Standalone explorations without a task retain the legacy profile.
func (s *ExplorationStore) AnalysisDomain() (string, error) {
	if s == nil || s.db == nil {
		return AnalysisDomainWebSecurity, nil
	}
	var value string
	err := s.db.QueryRow(`SELECT analysis_domain FROM tasks WHERE exploration_id=$1`, s.expID).Scan(&value)
	if err == sql.ErrNoRows {
		return AnalysisDomainWebSecurity, nil
	}
	if err != nil {
		return "", err
	}
	return NormalizeAnalysisDomain(value)
}
