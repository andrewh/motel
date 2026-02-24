// Package semconv loads and indexes OTel semantic convention definitions
// from the OpenTelemetry semantic convention YAML format.
package semconv

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	semconvdata "github.com/andrewh/motel/third_party/semconv"
	"gopkg.in/yaml.v3"
)

// groupsFile is the top-level structure of a semantic convention YAML file.
type groupsFile struct {
	Groups []Group `yaml:"groups"`
}

// Registry holds indexed semantic convention groups and attributes.
type Registry struct {
	groups    []Group
	byGroupID map[string]*Group
	byAttrID  map[string]*Attribute
	byDomain  map[string][]*Group
}

// Load parses all YAML files from the given filesystem into a Registry.
// Files in directories named "deprecated" are skipped.
func Load(fsys fs.FS) (*Registry, error) {
	var allGroups []Group

	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		// Skip deprecated directories.
		if containsDeprecated(path) {
			return nil
		}

		// Only process YAML files.
		ext := filepath.Ext(path)
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}

		data, readErr := fs.ReadFile(fsys, path)
		if readErr != nil {
			return fmt.Errorf("reading %s: %w", path, readErr)
		}

		var gf groupsFile
		if parseErr := yaml.Unmarshal(data, &gf); parseErr != nil {
			return fmt.Errorf("parsing %s: %w", path, parseErr)
		}

		// Tag each group with its domain from the directory path.
		domain := extractDomain(path)
		for i := range gf.Groups {
			gf.Groups[i].domain = domain
			allGroups = append(allGroups, gf.Groups[i])
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking filesystem: %w", err)
	}

	return buildRegistry(allGroups), nil
}

// LoadEmbedded loads the registry from the vendored semantic convention YAML files.
func LoadEmbedded() (*Registry, error) {
	sub, err := fs.Sub(semconvdata.ModelFS, "model")
	if err != nil {
		return nil, fmt.Errorf("accessing embedded model: %w", err)
	}
	return Load(sub)
}

// Group returns the group with the given ID, or nil if not found.
func (r *Registry) Group(id string) *Group {
	return r.byGroupID[id]
}

// Attribute returns the attribute with the given ID, or nil if not found.
func (r *Registry) Attribute(id string) *Attribute {
	return r.byAttrID[id]
}

// Domain returns all groups belonging to the given domain.
func (r *Registry) Domain(name string) []*Group {
	return r.byDomain[name]
}

// Domains returns a sorted list of all domain names.
func (r *Registry) Domains() []string {
	domains := make([]string, 0, len(r.byDomain))
	for d := range r.byDomain {
		domains = append(domains, d)
	}
	sort.Strings(domains)
	return domains
}

// Groups returns all groups in the registry.
func (r *Registry) Groups() []Group {
	return r.groups
}

// Merge combines two registries into a new one. Groups from other are appended
// after groups from r, so duplicate group IDs in other take precedence.
// Attribute references are re-resolved across the combined set.
func (r *Registry) Merge(other *Registry) *Registry {
	combined := make([]Group, 0, len(r.groups)+len(other.groups))
	for _, g := range r.groups {
		g.Attributes = append([]Attribute(nil), g.Attributes...)
		combined = append(combined, g)
	}
	for _, g := range other.groups {
		g.Attributes = append([]Attribute(nil), g.Attributes...)
		combined = append(combined, g)
	}
	return buildRegistry(combined)
}

// buildRegistry indexes groups and resolves attribute references.
func buildRegistry(groups []Group) *Registry {
	r := &Registry{
		groups:    groups,
		byGroupID: make(map[string]*Group, len(groups)),
		byAttrID:  make(map[string]*Attribute, len(groups)*4),
		byDomain:  make(map[string][]*Group),
	}

	// First pass: index all inline attributes (those with an ID, not a ref).
	for i := range r.groups {
		g := &r.groups[i]
		r.byGroupID[g.ID] = g
		if g.domain != "" {
			r.byDomain[g.domain] = append(r.byDomain[g.domain], g)
		}
		for j := range g.Attributes {
			attr := &g.Attributes[j]
			if attr.ID != "" && attr.Ref == "" {
				r.byAttrID[attr.ID] = attr
			}
		}
	}

	// Second pass: resolve ref attributes.
	for i := range r.groups {
		for j := range r.groups[i].Attributes {
			attr := &r.groups[i].Attributes[j]
			if attr.Ref == "" {
				continue
			}
			resolveRef(attr, r.byAttrID)
		}
	}

	return r
}

// resolveRef merges a ref attribute with its definition.
// The ref's Brief and Note override if non-empty. Type, Examples, and Stability
// come from the definition. RequirementLevel and SamplingRelevant come from the ref.
func resolveRef(attr *Attribute, index map[string]*Attribute) {
	def, ok := index[attr.Ref]
	if !ok {
		// Unresolved ref: populate ID from Ref so lookups work.
		attr.ID = attr.Ref
		return
	}

	// Copy definition fields.
	attr.ID = def.ID
	attr.Type = def.Type
	attr.Stability = def.Stability
	attr.Examples = def.Examples
	attr.Deprecated = def.Deprecated

	// Apply ref overrides: Brief and Note from the ref take precedence if non-empty.
	// RequirementLevel and SamplingRelevant are already set from the ref.
	if attr.Brief == "" {
		attr.Brief = def.Brief
	}
	if attr.Note == "" {
		attr.Note = def.Note
	}
}

// containsDeprecated checks if a file path includes a "deprecated" directory component.
func containsDeprecated(path string) bool {
	for part := range strings.SplitSeq(filepath.ToSlash(path), "/") {
		if part == "deprecated" {
			return true
		}
	}
	return false
}

// extractDomain returns the first directory component of a path.
func extractDomain(path string) string {
	parts := strings.SplitN(filepath.ToSlash(path), "/", 2)
	if len(parts) > 1 {
		return parts[0]
	}
	return ""
}
