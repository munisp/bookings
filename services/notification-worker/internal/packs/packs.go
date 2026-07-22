// Package packs loads industry workflow packs (SPEC-CRM §C) from a mounted
// directory of YAML files (default /industries) so the ApplyIndustryPack
// activity can seed offerings, knowledge documents and terminology. The
// schema mirrors identity-service's internal/packs (duplicated per
// service-boundary rules — no shared top-level package).
package packs

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultDir is used when INDUSTRIES_DIR is not set.
const DefaultDir = "/industries"

// DefaultIndustry is applied when no industry is given.
const DefaultIndustry = "salon"

// BookingPolicy mirrors the pack bookingPolicy block.
type BookingPolicy struct {
	DepositPercent          int   `yaml:"depositPercent" json:"depositPercent"`
	NoShowFeeCents          int64 `yaml:"noShowFeeCents" json:"noShowFeeCents"`
	PhoneConfirmation       bool  `yaml:"phoneConfirmation" json:"phoneConfirmation"`
	IntakeRequired          bool  `yaml:"intakeRequired" json:"intakeRequired"`
	CancellationWindowHours int   `yaml:"cancellationWindowHours" json:"cancellationWindowHours"`
}

// Offering is a catalog seed entry of a pack.
type Offering struct {
	Name        string `yaml:"name" json:"name"`
	DurationMin int    `yaml:"duration_min" json:"duration_min"`
	BufferMin   int    `yaml:"buffer_min" json:"buffer_min"`
	PriceCents  int64  `yaml:"price_cents" json:"price_cents"`
	Capacity    int    `yaml:"capacity" json:"capacity"`
}

// KnowledgeDoc is a knowledge-base seed document of a pack.
type KnowledgeDoc struct {
	Title string `yaml:"title" json:"title"`
	Body  string `yaml:"body" json:"body"`
}

// Pack is one industry pack definition (industries/<id>.yaml). Fields not
// needed by the worker (agentPersona, dashboardLabels, ...) are still parsed
// so a malformed pack fails validation here exactly as in identity-service.
type Pack struct {
	ID               string            `yaml:"id" json:"id"`
	DisplayName      string            `yaml:"displayName" json:"displayName"`
	Terminology      map[string]string `yaml:"terminology" json:"terminology"`
	AgentPersona     string            `yaml:"agentPersona" json:"agentPersona"`
	BookingPolicy    BookingPolicy     `yaml:"bookingPolicy" json:"bookingPolicy"`
	TemporalWorkflow string            `yaml:"temporalWorkflow" json:"temporalWorkflow"`
	Offerings        []Offering        `yaml:"offerings" json:"offerings"`
	KnowledgeSeed    []KnowledgeDoc    `yaml:"knowledgeSeed" json:"knowledgeSeed"`
	DashboardLabels  map[string]string `yaml:"dashboardLabels" json:"dashboardLabels"`
}

// Registry is an in-memory cache of packs keyed by pack id.
type Registry struct {
	packs map[string]Pack
}

// Load reads every *.yaml / *.yml file in dir and returns the cached
// registry. A missing directory yields an empty registry (the worker still
// boots; ApplyIndustryPack then no-ops). Any invalid pack file is fatal.
func Load(dir string) (*Registry, error) {
	if dir == "" {
		dir = DefaultDir
	}
	r := &Registry{packs: map[string]Pack{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return r, nil
		}
		return nil, fmt.Errorf("read industries dir %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read pack %s: %w", path, err)
		}
		var p Pack
		if err := yaml.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("parse pack %s: %w", path, err)
		}
		if p.ID == "" || p.DisplayName == "" || p.TemporalWorkflow == "" {
			return nil, fmt.Errorf("invalid pack %s: id, displayName and temporalWorkflow are required", path)
		}
		if _, dup := r.packs[p.ID]; dup {
			return nil, fmt.Errorf("duplicate pack id %q (%s)", p.ID, path)
		}
		r.packs[p.ID] = p
	}
	return r, nil
}

// Get returns the pack for an id.
func (r *Registry) Get(id string) (Pack, bool) {
	p, ok := r.packs[id]
	return p, ok
}

// IDs returns the sorted list of loaded pack ids.
func (r *Registry) IDs() []string {
	ids := make([]string, 0, len(r.packs))
	for id := range r.packs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
