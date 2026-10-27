// Package updates checks the fixed official release catalog and prepares an
// auditable manual host handoff. It never installs, executes, or restarts code.
package updates

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

const Repository = "xiaoqiqixi/betteridcsys"
const RepositoryID int64 = 1418618420

var (
	ErrUnavailable  = errors.New("official release metadata unavailable")
	ErrBusy         = errors.New("release check already in progress")
	ErrStale        = errors.New("release selection changed")
	ErrIncompatible = errors.New("release incompatible or not newer")
)

type Current struct {
	CoreVersion          string `json:"core_version"`
	SMTPVersion          string `json:"smtp_version"`
	SMTPStatus           string `json:"smtp_status"`
	SMTPProtocol         string `json:"smtp_protocol"`
	SMTPCoreMin          string `json:"smtp_core_min"`
	SMTPCoreMaxExclusive string `json:"smtp_core_max_exclusive"`
	PluginPolicyRevision uint64 `json:"plugin_policy_revision"`
}
type Source struct {
	Repository string `json:"repository"`
	Status     string `json:"status"`
}
type Asset struct {
	Name   string `json:"name"`
	ID     int64  `json:"id"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type Candidate struct {
	DistributionFormat string   `json:"distribution_format"`
	Component          string   `json:"component"`
	Tag                string   `json:"tag"`
	Version            string   `json:"version"`
	ReleaseURL         string   `json:"release_url"`
	Notes              string   `json:"notes"`
	PublishedAt        string   `json:"published_at"`
	SourceSHA          string   `json:"source_sha"`
	ReleaseID          int64    `json:"release_id"`
	RunID              int64    `json:"run_id"`
	RunAttempt         int64    `json:"run_attempt"`
	Compatible         bool     `json:"compatible"`
	UpdateAvailable    bool     `json:"update_available"`
	Preconditions      []string `json:"preconditions"`
	Assets             []Asset  `json:"assets"`
	CatalogSHA256      string   `json:"catalog_sha256"`
}
type View struct {
	Current   Current     `json:"current"`
	Source    Source      `json:"source"`
	CheckedAt string      `json:"checked_at"`
	Items     []Candidate `json:"items"`
	Mode      string      `json:"mode"`
}
type Intent struct {
	Schema               int     `json:"schema"`
	DistributionFormat   string  `json:"distribution_format"`
	RequestID            string  `json:"request_id"`
	ActorID              string  `json:"actor_id"`
	CreatedAt            string  `json:"created_at"`
	ExpiresAt            string  `json:"expires_at"`
	Repository           string  `json:"repository"`
	RepositoryID         int64   `json:"repository_id"`
	Component            string  `json:"component"`
	Tag                  string  `json:"tag"`
	Version              string  `json:"version"`
	SourceSHA            string  `json:"source_sha"`
	ReleaseID            int64   `json:"release_id"`
	RunID                int64   `json:"run_id"`
	RunAttempt           int64   `json:"run_attempt"`
	CurrentCoreVersion   string  `json:"current_core_version"`
	CurrentPluginVersion string  `json:"current_plugin_version"`
	PluginPolicyRevision uint64  `json:"plugin_policy_revision"`
	Assets               []Asset `json:"assets"`
}
type Handoff struct {
	Schema int    `json:"schema"`
	Status string `json:"status"`
	Intent Intent `json:"intent"`
}

type Service struct {
	mu       sync.Mutex
	checking bool
	view     View
	client   *githubClient
	current  func() Current
}

// New accepts only a credential for the fixed official API. An empty credential
// works for public releases; a private repository normally reports unavailable.
func New(token string, current func() Current) *Service {
	return &Service{client: newGitHubClient(token), current: current, view: View{Source: Source{Repository, "not_checked"}, Items: []Candidate{}, Mode: "manual_host_handoff"}}
}

func (s *Service) Snapshot() View {
	if s == nil {
		return View{Source: Source{Repository, "unavailable"}, Items: []Candidate{}, Mode: "manual_host_handoff"}
	}
	s.mu.Lock()
	v := s.view
	v.Items = append([]Candidate{}, v.Items...)
	for i := range v.Items {
		v.Items[i].Assets = append([]Asset{}, v.Items[i].Assets...)
		v.Items[i].Preconditions = append([]string{}, v.Items[i].Preconditions...)
	}
	s.mu.Unlock()
	current := s.current()
	if v.CheckedAt != "" && current != v.Current {
		v.Source.Status = "not_checked"
		v.CheckedAt = ""
		v.Items = []Candidate{}
	}
	v.Current = current
	return v
}
func (s *Service) Check(ctx context.Context) (View, error) {
	if s == nil {
		return s.Snapshot(), ErrUnavailable
	}
	s.mu.Lock()
	if s.checking {
		s.mu.Unlock()
		return s.Snapshot(), ErrBusy
	}
	s.checking = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.checking = false; s.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	current := s.current()
	items, err := s.client.catalog(ctx, current)
	v := View{Current: current, Source: Source{Repository, "available"}, CheckedAt: time.Now().UTC().Format(time.RFC3339), Items: items, Mode: "manual_host_handoff"}
	if err != nil {
		v.Source.Status = "unavailable"
		v.Items = []Candidate{}
	}
	s.mu.Lock()
	s.view = v
	s.mu.Unlock()
	if err != nil {
		return v, ErrUnavailable
	}
	return v, nil
}

// Prepare rechecks every official fact, then compares against the administrator's
// displayed selection. Persist its audit before returning the resulting handoff.
func (s *Service) Prepare(ctx context.Context, actor, component, tag, sourceSHA string, releaseID int64, catalogSHA string) (Handoff, error) {
	if s == nil {
		return Handoff{}, ErrUnavailable
	}
	if !actorPattern.MatchString(actor) || (component != "core" && component != "smtp") || !validTag(tag, component) || !commitPattern.MatchString(sourceSHA) || releaseID <= 0 || !hashPattern.MatchString(catalogSHA) {
		return Handoff{}, ErrStale
	}
	v, err := s.Check(ctx)
	if err != nil {
		return Handoff{}, err
	}
	for _, candidate := range v.Items {
		if candidate.Component != component || candidate.Tag != tag {
			continue
		}
		if candidate.SourceSHA != sourceSHA || candidate.ReleaseID != releaseID || candidate.CatalogSHA256 != catalogSHA {
			return Handoff{}, ErrStale
		}
		if !candidate.Compatible || !candidate.UpdateAvailable {
			return Handoff{}, ErrIncompatible
		}
		if s.current() != v.Current {
			return Handoff{}, ErrStale
		}
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return Handoff{}, ErrUnavailable
		}
		now := time.Now().UTC().Truncate(time.Second)
		return Handoff{Schema: 2, Status: "awaiting_host_operator", Intent: Intent{Schema: 2, DistributionFormat: candidate.DistributionFormat, RequestID: hex.EncodeToString(nonce[:]), ActorID: actor, CreatedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(30 * time.Minute).Format(time.RFC3339), Repository: Repository, RepositoryID: RepositoryID, Component: component, Tag: tag, Version: candidate.Version, SourceSHA: candidate.SourceSHA, ReleaseID: candidate.ReleaseID, RunID: candidate.RunID, RunAttempt: candidate.RunAttempt, CurrentCoreVersion: v.Current.CoreVersion, CurrentPluginVersion: v.Current.SMTPVersion, PluginPolicyRevision: v.Current.PluginPolicyRevision, Assets: candidate.Assets}}, nil
	}
	return Handoff{}, ErrStale
}

func catalogDigest(candidate Candidate) string {
	// Bind all executable identity facts shown by the previous explicit check.
	// Release note text may change without changing the selected executable.
	raw, _ := json.Marshal(struct {
		DistributionFormat string
		Component          string
		Tag                string
		SourceSHA          string
		ReleaseID          int64
		RunID              int64
		RunAttempt         int64
		Assets             []Asset
	}{candidate.DistributionFormat, candidate.Component, candidate.Tag, candidate.SourceSHA, candidate.ReleaseID, candidate.RunID, candidate.RunAttempt, candidate.Assets})
	return digestBytes(raw)
}
