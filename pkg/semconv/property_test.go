// Property-based tests for the semantic convention registry
// Covers index consistency, lookup correctness, merge properties, and domain indexing
package semconv

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"pgregory.net/rapid"
)

// --- Generators ---

// genAttribute generates a random inline attribute (not a ref).
func genAttribute(t *rapid.T, label string) Attribute {
	id := rapid.SampledFrom([]string{
		"test.name", "test.count", "test.flag", "test.size", "test.path",
	}).Draw(t, label+"-id")
	typ := rapid.SampledFrom([]string{"string", "int", "boolean", "double"}).Draw(t, label+"-type")
	return Attribute{
		ID:    id,
		Type:  AttributeType{Value: typ},
		Brief: fmt.Sprintf("Brief for %s", id),
	}
}

// genGroup generates a random group with 1-3 inline attributes.
func genGroup(t *rapid.T, label string, domain string) Group {
	id := fmt.Sprintf("registry.%s", label)
	nAttrs := rapid.IntRange(1, 3).Draw(t, label+"-nAttrs")
	attrs := make([]Attribute, nAttrs)
	for i := range nAttrs {
		attrs[i] = genAttribute(t, fmt.Sprintf("%s-attr%d", label, i))
	}
	return Group{
		ID:         id,
		Type:       "attribute_group",
		Brief:      fmt.Sprintf("Group %s", label),
		Attributes: attrs,
		domain:     domain,
	}
}

// --- Registry index consistency ---

func TestProperty_Registry_GroupLookupConsistent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		nGroups := rapid.IntRange(1, 5).Draw(t, "nGroups")
		groups := make([]Group, nGroups)
		for i := range nGroups {
			groups[i] = genGroup(t, fmt.Sprintf("g%d", i), "testdomain")
		}

		reg := buildRegistry(groups)

		for _, g := range groups {
			looked := reg.Group(g.ID)
			if looked == nil {
				t.Fatalf("Group(%q) returned nil", g.ID)
			}
			if looked.ID != g.ID {
				t.Fatalf("Group(%q) returned ID %q", g.ID, looked.ID)
			}
		}
	})
}

func TestProperty_Registry_AttributeLookupConsistent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		nGroups := rapid.IntRange(1, 3).Draw(t, "nGroups")
		groups := make([]Group, nGroups)
		for i := range nGroups {
			groups[i] = genGroup(t, fmt.Sprintf("g%d", i), "testdomain")
		}

		reg := buildRegistry(groups)

		for _, g := range groups {
			for _, attr := range g.Attributes {
				looked := reg.Attribute(attr.ID)
				if looked == nil {
					t.Fatalf("Attribute(%q) returned nil", attr.ID)
				}
				if looked.ID != attr.ID {
					t.Fatalf("Attribute(%q) returned ID %q", attr.ID, looked.ID)
				}
			}
		}
	})
}

func TestProperty_Registry_NonexistentReturnsNil(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		groups := []Group{genGroup(t, "g0", "domain")}
		reg := buildRegistry(groups)

		bogus := rapid.SampledFrom([]string{
			"nonexistent.group", "fake.attr", "missing.id",
		}).Draw(t, "bogus")

		if reg.Group(bogus) != nil {
			t.Fatalf("Group(%q) should be nil", bogus)
		}
		if reg.Attribute(bogus) != nil {
			t.Fatalf("Attribute(%q) should be nil", bogus)
		}
	})
}

// --- Domain indexing ---

func TestProperty_Registry_DomainsSorted(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		nDomains := rapid.IntRange(2, 5).Draw(t, "nDomains")
		domainNames := []string{"alpha", "bravo", "charlie", "delta", "echo"}[:nDomains]

		var groups []Group
		for i, d := range domainNames {
			groups = append(groups, genGroup(t, fmt.Sprintf("d%d", i), d))
		}

		reg := buildRegistry(groups)
		domains := reg.Domains()

		if !sort.StringsAreSorted(domains) {
			t.Fatalf("Domains() not sorted: %v", domains)
		}
	})
}

func TestProperty_Registry_DomainGroupsComplete(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		domain := rapid.SampledFrom([]string{"http", "db", "rpc"}).Draw(t, "domain")
		nGroups := rapid.IntRange(1, 4).Draw(t, "nGroups")

		groups := make([]Group, nGroups)
		for i := range nGroups {
			groups[i] = genGroup(t, fmt.Sprintf("d%d", i), domain)
		}

		reg := buildRegistry(groups)
		domainGroups := reg.Domain(domain)

		if len(domainGroups) != nGroups {
			t.Fatalf("Domain(%q) returned %d groups, want %d", domain, len(domainGroups), nGroups)
		}
	})
}

// --- Merge properties ---

func TestProperty_Registry_MergeContainsBoth(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		g1 := genGroup(t, "a", "domA")
		g2 := genGroup(t, "b", "domB")

		regA := buildRegistry([]Group{g1})
		regB := buildRegistry([]Group{g2})

		merged := regA.Merge(regB)

		if merged.Group(g1.ID) == nil {
			t.Fatalf("merged missing group %q from regA", g1.ID)
		}
		if merged.Group(g2.ID) == nil {
			t.Fatalf("merged missing group %q from regB", g2.ID)
		}
		if len(merged.Groups()) != 2 {
			t.Fatalf("merged has %d groups, want 2", len(merged.Groups()))
		}
	})
}

func TestProperty_Registry_MergeDoesNotMutateOriginal(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		g1 := genGroup(t, "a", "dom")
		g2 := genGroup(t, "b", "dom")

		regA := buildRegistry([]Group{g1})
		regB := buildRegistry([]Group{g2})

		origACount := len(regA.Groups())
		origBCount := len(regB.Groups())

		_ = regA.Merge(regB)

		if len(regA.Groups()) != origACount {
			t.Fatalf("regA mutated: was %d groups, now %d", origACount, len(regA.Groups()))
		}
		if len(regB.Groups()) != origBCount {
			t.Fatalf("regB mutated: was %d groups, now %d", origBCount, len(regB.Groups()))
		}
	})
}

// --- Ref resolution ---

func TestProperty_Registry_RefResolution(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		attrType := rapid.SampledFrom([]string{"string", "int", "boolean"}).Draw(t, "attrType")
		stability := rapid.SampledFrom([]string{"stable", "experimental"}).Draw(t, "stability")

		defGroup := Group{
			ID:   "registry.defs",
			Type: "attribute_group",
			Attributes: []Attribute{{
				ID:        "my.attr",
				Type:      AttributeType{Value: attrType},
				Brief:     "Definition brief",
				Stability: stability,
			}},
		}

		refBrief := rapid.SampledFrom([]string{"", "Override brief"}).Draw(t, "refBrief")
		refGroup := Group{
			ID:   "registry.refs",
			Type: "attribute_group",
			Attributes: []Attribute{{
				Ref:   "my.attr",
				Brief: refBrief,
			}},
		}

		reg := buildRegistry([]Group{defGroup, refGroup})

		g := reg.Group("registry.refs")
		if g == nil {
			t.Fatal("ref group not found")
		}
		attr := g.Attributes[0]

		if attr.ID != "my.attr" {
			t.Fatalf("resolved ID: got %q, want %q", attr.ID, "my.attr")
		}
		if attr.Type.Value != attrType {
			t.Fatalf("resolved Type: got %q, want %q", attr.Type.Value, attrType)
		}
		if attr.Stability != stability {
			t.Fatalf("resolved Stability: got %q, want %q", attr.Stability, stability)
		}

		if refBrief != "" && attr.Brief != refBrief {
			t.Fatalf("ref brief should override: got %q, want %q", attr.Brief, refBrief)
		}
		if refBrief == "" && attr.Brief != "Definition brief" {
			t.Fatalf("empty ref brief should fall back to def: got %q", attr.Brief)
		}
	})
}

// --- Load with filesystem ---

func TestProperty_Load_GroupCountMatchesInput(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		nGroups := rapid.IntRange(1, 4).Draw(t, "nGroups")
		var b strings.Builder
		b.WriteString("groups:\n")
		for i := range nGroups {
			fmt.Fprintf(&b, "  - id: test.group%d\n    type: attribute_group\n    brief: 'Group %d.'\n", i, i)
		}
		yamlContent := b.String()

		fsys := fstest.MapFS{
			"test/registry.yaml": &fstest.MapFile{Data: []byte(yamlContent)},
		}

		reg, err := Load(fsys)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(reg.Groups()) != nGroups {
			t.Fatalf("got %d groups, want %d", len(reg.Groups()), nGroups)
		}
	})
}
