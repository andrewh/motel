// Unit tests for semantic convention registry types, parsing, and loading.
// Uses inline YAML and fstest.MapFS for isolation; real ModelFS for smoke tests.
package semconv

import (
	"slices"
	"sort"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// --- Phase 1: Type Unmarshalling ---

func TestAttributeType_UnmarshalYAML_Scalar(t *testing.T) {
	t.Parallel()
	cases := []string{"string", "int", "boolean", "double", "string[]", "int[]", "template[string]", "template[string[]]"}
	for _, tc := range cases {
		var at AttributeType
		err := yaml.Unmarshal([]byte(tc), &at)
		require.NoError(t, err)
		assert.Equal(t, tc, at.Value)
		assert.Empty(t, at.Members)
	}
}

func TestAttributeType_UnmarshalYAML_Enum(t *testing.T) {
	t.Parallel()
	input := `
members:
  - id: get
    value: "GET"
    brief: 'GET method.'
    stability: stable
  - id: post
    value: "POST"
    brief: 'POST method.'
    stability: stable
`
	var at AttributeType
	err := yaml.Unmarshal([]byte(input), &at)
	require.NoError(t, err)
	assert.Equal(t, "enum", at.Value)
	require.Len(t, at.Members, 2)
	assert.Equal(t, "get", at.Members[0].ID)
	assert.Equal(t, "GET", at.Members[0].Value)
	assert.Equal(t, "stable", at.Members[0].Stability)
	assert.Equal(t, "post", at.Members[1].ID)
}

func TestRequirementLevel_UnmarshalYAML_Scalar(t *testing.T) {
	t.Parallel()
	for _, level := range []string{"required", "recommended", "opt_in"} {
		var rl RequirementLevel
		err := yaml.Unmarshal([]byte(level), &rl)
		require.NoError(t, err)
		assert.Equal(t, level, rl.Level)
		assert.Empty(t, rl.Explanation)
	}
}

func TestRequirementLevel_UnmarshalYAML_Conditional(t *testing.T) {
	t.Parallel()
	input := `conditionally_required: If using a non-default port.`
	var rl RequirementLevel
	err := yaml.Unmarshal([]byte(input), &rl)
	require.NoError(t, err)
	assert.Equal(t, "conditionally_required", rl.Level)
	assert.Equal(t, "If using a non-default port.", rl.Explanation)
}

func TestExamples_UnmarshalYAML_Scalar(t *testing.T) {
	t.Parallel()
	var ex Examples
	err := yaml.Unmarshal([]byte(`3495`), &ex)
	require.NoError(t, err)
	require.Len(t, ex.Values, 1)
	assert.Equal(t, 3495, ex.Values[0])
}

func TestExamples_UnmarshalYAML_Sequence(t *testing.T) {
	t.Parallel()
	var ex Examples
	err := yaml.Unmarshal([]byte(`["GET", "POST", "HEAD"]`), &ex)
	require.NoError(t, err)
	require.Len(t, ex.Values, 3)
	assert.Equal(t, "GET", ex.Values[0])
}

func TestExamples_UnmarshalYAML_NestedSequence(t *testing.T) {
	t.Parallel()
	input := `[["application/json"], ["1.2.3.4", "1.2.3.5"]]`
	var ex Examples
	err := yaml.Unmarshal([]byte(input), &ex)
	require.NoError(t, err)
	require.Len(t, ex.Values, 2)
}

// --- Phase 2: File Parsing ---

func TestParseGroupsFile(t *testing.T) {
	t.Parallel()
	input := `
groups:
  - id: registry.test
    type: attribute_group
    display_name: Test Attributes
    brief: 'Test group.'
    attributes:
      - id: test.name
        type: string
        brief: 'A test attribute.'
        stability: stable
        examples: ["foo", "bar"]
`
	var gf groupsFile
	err := yaml.Unmarshal([]byte(input), &gf)
	require.NoError(t, err)
	require.Len(t, gf.Groups, 1)
	g := gf.Groups[0]
	assert.Equal(t, "registry.test", g.ID)
	assert.Equal(t, "attribute_group", g.Type)
	assert.Equal(t, "Test Attributes", g.DisplayName)
	require.Len(t, g.Attributes, 1)
	attr := g.Attributes[0]
	assert.Equal(t, "test.name", attr.ID)
	assert.Equal(t, "string", attr.Type.Value)
	assert.Equal(t, "stable", attr.Stability)
	require.Len(t, attr.Examples.Values, 2)
}

func TestParseGroupsFile_EnumAttribute(t *testing.T) {
	t.Parallel()
	input := `
groups:
  - id: registry.http
    type: attribute_group
    brief: 'HTTP attributes.'
    attributes:
      - id: http.request.method
        type:
          members:
            - id: get
              value: "GET"
              brief: 'GET method.'
              stability: stable
            - id: post
              value: "POST"
              brief: 'POST method.'
              stability: stable
        brief: 'HTTP request method.'
        examples: ["GET", "POST"]
`
	var gf groupsFile
	err := yaml.Unmarshal([]byte(input), &gf)
	require.NoError(t, err)
	attr := gf.Groups[0].Attributes[0]
	assert.Equal(t, "enum", attr.Type.Value)
	require.Len(t, attr.Type.Members, 2)
	assert.Equal(t, "GET", attr.Type.Members[0].Value)
}

func TestParseGroupsFile_RefAttributes(t *testing.T) {
	t.Parallel()
	input := `
groups:
  - id: attributes.db.client
    type: attribute_group
    brief: 'DB client attributes.'
    attributes:
      - ref: server.address
        brief: 'Name of the database host.'
      - ref: server.port
        requirement_level:
          conditionally_required: If using a non-default port.
`
	var gf groupsFile
	err := yaml.Unmarshal([]byte(input), &gf)
	require.NoError(t, err)
	attrs := gf.Groups[0].Attributes
	require.Len(t, attrs, 2)
	assert.Equal(t, "server.address", attrs[0].Ref)
	assert.Equal(t, "Name of the database host.", attrs[0].Brief)
	assert.Equal(t, "conditionally_required", attrs[1].RequirementLevel.Level)
}

// --- Phase 3: Registry Loading ---

func TestLoad_SingleFile(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"test/registry.yaml": &fstest.MapFile{
			Data: []byte(`
groups:
  - id: registry.test
    type: attribute_group
    brief: 'Test group.'
    attributes:
      - id: test.name
        type: string
        brief: 'A test name.'
        stability: stable
        examples: ["foo"]
      - id: test.count
        type: int
        brief: 'A test count.'
        examples: [1, 2, 3]
`),
		},
	}
	reg, err := Load(fsys)
	require.NoError(t, err)

	g := reg.Group("registry.test")
	require.NotNil(t, g)
	assert.Equal(t, "attribute_group", g.Type)

	attr := reg.Attribute("test.name")
	require.NotNil(t, attr)
	assert.Equal(t, "string", attr.Type.Value)

	attr2 := reg.Attribute("test.count")
	require.NotNil(t, attr2)
	assert.Equal(t, "int", attr2.Type.Value)

	assert.Nil(t, reg.Group("nonexistent"))
	assert.Nil(t, reg.Attribute("nonexistent"))
}

func TestLoad_RefResolution(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"server/registry.yaml": &fstest.MapFile{
			Data: []byte(`
groups:
  - id: registry.server
    type: attribute_group
    brief: 'Server attributes.'
    attributes:
      - id: server.address
        type: string
        brief: 'Server address.'
        stability: stable
        examples: ["example.com"]
`),
		},
		"db/common.yaml": &fstest.MapFile{
			Data: []byte(`
groups:
  - id: attributes.db.client
    type: attribute_group
    brief: 'DB client attributes.'
    attributes:
      - ref: server.address
        brief: 'Name of the database host.'
        requirement_level:
          conditionally_required: If applicable.
`),
		},
	}
	reg, err := Load(fsys)
	require.NoError(t, err)

	g := reg.Group("attributes.db.client")
	require.NotNil(t, g)
	require.Len(t, g.Attributes, 1)

	attr := g.Attributes[0]
	// Ref's brief overrides the definition's brief.
	assert.Equal(t, "Name of the database host.", attr.Brief)
	// Type comes from the definition.
	assert.Equal(t, "string", attr.Type.Value)
	// Stability comes from the definition.
	assert.Equal(t, "stable", attr.Stability)
	// RequirementLevel comes from the ref.
	assert.Equal(t, "conditionally_required", attr.RequirementLevel.Level)
	// ID is populated from the ref.
	assert.Equal(t, "server.address", attr.ID)
}

func TestLoad_UnresolvedRef(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"db/common.yaml": &fstest.MapFile{
			Data: []byte(`
groups:
  - id: attributes.db.client
    type: attribute_group
    brief: 'DB client attributes.'
    attributes:
      - ref: nonexistent.attribute
        brief: 'Missing ref.'
`),
		},
	}
	reg, err := Load(fsys)
	require.NoError(t, err)

	g := reg.Group("attributes.db.client")
	require.NotNil(t, g)
	require.Len(t, g.Attributes, 1)
	// Unresolved ref keeps ref ID but has no type populated.
	attr := g.Attributes[0]
	assert.Equal(t, "nonexistent.attribute", attr.ID)
	assert.Equal(t, "Missing ref.", attr.Brief)
	assert.Empty(t, attr.Type.Value)
}

func TestLoad_DomainIndex(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"http/registry.yaml": &fstest.MapFile{
			Data: []byte(`
groups:
  - id: registry.http
    type: attribute_group
    brief: 'HTTP attributes.'
    attributes:
      - id: http.method
        type: string
        brief: 'HTTP method.'
`),
		},
		"http/metrics.yaml": &fstest.MapFile{
			Data: []byte(`
groups:
  - id: metric.http.duration
    type: metric
    metric_name: http.server.duration
    brief: 'Duration.'
    instrument: histogram
    unit: s
`),
		},
		"db/registry.yaml": &fstest.MapFile{
			Data: []byte(`
groups:
  - id: registry.db
    type: attribute_group
    brief: 'DB attributes.'
`),
		},
	}
	reg, err := Load(fsys)
	require.NoError(t, err)

	domains := reg.Domains()
	sort.Strings(domains)
	assert.Equal(t, []string{"db", "http"}, domains)

	httpGroups := reg.Domain("http")
	assert.Len(t, httpGroups, 2)

	dbGroups := reg.Domain("db")
	assert.Len(t, dbGroups, 1)

	assert.Empty(t, reg.Domain("nonexistent"))
}

func TestLoad_SkipsDeprecated(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"http/registry.yaml": &fstest.MapFile{
			Data: []byte(`
groups:
  - id: registry.http
    type: attribute_group
    brief: 'HTTP attributes.'
`),
		},
		"http/deprecated/registry-deprecated.yaml": &fstest.MapFile{
			Data: []byte(`
groups:
  - id: registry.http.deprecated
    type: attribute_group
    brief: 'Deprecated HTTP attributes.'
`),
		},
	}
	reg, err := Load(fsys)
	require.NoError(t, err)

	assert.NotNil(t, reg.Group("registry.http"))
	assert.Nil(t, reg.Group("registry.http.deprecated"))
}

func TestGroups(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"http/registry.yaml": &fstest.MapFile{
			Data: []byte(`
groups:
  - id: registry.http
    type: attribute_group
    brief: 'HTTP attributes.'
  - id: metric.http.duration
    type: metric
    brief: 'Duration.'
`),
		},
	}
	reg, err := Load(fsys)
	require.NoError(t, err)
	assert.Len(t, reg.Groups(), 2)
}

// --- Phase 4: Embedded Smoke Tests ---

func TestLoadEmbedded(t *testing.T) {
	t.Parallel()
	reg, err := LoadEmbedded()
	require.NoError(t, err)

	// Should have a substantial number of groups and attributes.
	assert.Greater(t, len(reg.Groups()), 100)
	assert.Greater(t, len(reg.Domains()), 20)

	// Known groups should exist.
	assert.NotNil(t, reg.Group("registry.http"))
	assert.NotNil(t, reg.Group("registry.service"))

	// Known attributes should exist.
	assert.NotNil(t, reg.Attribute("http.request.method"))
	assert.NotNil(t, reg.Attribute("service.name"))

	// Known domains should be present.
	domains := reg.Domains()
	assert.True(t, slices.Contains(domains, "http"))
	assert.True(t, slices.Contains(domains, "db"))
	assert.True(t, slices.Contains(domains, "service"))
}

func TestLoadEmbedded_KnownEnum(t *testing.T) {
	t.Parallel()
	reg, err := LoadEmbedded()
	require.NoError(t, err)

	attr := reg.Attribute("http.request.method")
	require.NotNil(t, attr)
	assert.Equal(t, "enum", attr.Type.Value)
	require.Greater(t, len(attr.Type.Members), 5)

	// Verify known enum members exist.
	memberValues := make(map[string]bool)
	for _, m := range attr.Type.Members {
		if s, ok := m.Value.(string); ok {
			memberValues[s] = true
		}
	}
	assert.True(t, memberValues["GET"])
	assert.True(t, memberValues["POST"])
}

// --- Helpers for internal types ---

func TestLoad_EmptyFS(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{}
	reg, err := Load(fsys)
	require.NoError(t, err)
	assert.Empty(t, reg.Groups())
	assert.Empty(t, reg.Domains())
}

// --- Phase 5: Merge ---

func TestMerge_CombinesGroups(t *testing.T) {
	t.Parallel()
	fsA := fstest.MapFS{
		"http/registry.yaml": &fstest.MapFile{
			Data: []byte(`
groups:
  - id: registry.http
    type: attribute_group
    brief: 'HTTP attributes.'
    attributes:
      - id: http.method
        type: string
        brief: 'HTTP method.'
`),
		},
	}
	fsB := fstest.MapFS{
		"myapp/registry.yaml": &fstest.MapFile{
			Data: []byte(`
groups:
  - id: registry.myapp
    type: attribute_group
    brief: 'My app attributes.'
    attributes:
      - id: myapp.request_id
        type: string
        brief: 'Request ID.'
`),
		},
	}

	regA, err := Load(fsA)
	require.NoError(t, err)
	regB, err := Load(fsB)
	require.NoError(t, err)

	merged := regA.Merge(regB)
	assert.NotNil(t, merged.Group("registry.http"))
	assert.NotNil(t, merged.Group("registry.myapp"))
	assert.NotNil(t, merged.Attribute("http.method"))
	assert.NotNil(t, merged.Attribute("myapp.request_id"))
	assert.Len(t, merged.Groups(), 2)
}

func TestMerge_UserOverridesEmbedded(t *testing.T) {
	t.Parallel()
	fsBase := fstest.MapFS{
		"http/registry.yaml": &fstest.MapFile{
			Data: []byte(`
groups:
  - id: registry.http
    type: attribute_group
    brief: 'Upstream HTTP.'
    attributes:
      - id: http.method
        type: string
        brief: 'HTTP method.'
`),
		},
	}
	fsUser := fstest.MapFS{
		"http/registry.yaml": &fstest.MapFile{
			Data: []byte(`
groups:
  - id: registry.http
    type: attribute_group
    brief: 'Custom HTTP.'
    attributes:
      - id: http.method
        type: string
        brief: 'Custom method.'
      - id: http.custom_header
        type: string
        brief: 'Custom header.'
`),
		},
	}

	base, err := Load(fsBase)
	require.NoError(t, err)
	user, err := Load(fsUser)
	require.NoError(t, err)

	merged := base.Merge(user)
	g := merged.Group("registry.http")
	require.NotNil(t, g)
	assert.Equal(t, "Custom HTTP.", g.Brief)
	assert.Len(t, g.Attributes, 2)
	assert.NotNil(t, merged.Attribute("http.custom_header"))
}

func TestMerge_RefsResolveAcrossRegistries(t *testing.T) {
	t.Parallel()
	fsBase := fstest.MapFS{
		"server/registry.yaml": &fstest.MapFile{
			Data: []byte(`
groups:
  - id: registry.server
    type: attribute_group
    brief: 'Server attributes.'
    attributes:
      - id: server.address
        type: string
        brief: 'Server address.'
        stability: stable
        examples: ["example.com"]
`),
		},
	}
	fsUser := fstest.MapFS{
		"myapp/registry.yaml": &fstest.MapFile{
			Data: []byte(`
groups:
  - id: registry.myapp
    type: attribute_group
    brief: 'My app.'
    attributes:
      - ref: server.address
        brief: 'Upstream host.'
`),
		},
	}

	base, err := Load(fsBase)
	require.NoError(t, err)
	user, err := Load(fsUser)
	require.NoError(t, err)

	merged := base.Merge(user)
	g := merged.Group("registry.myapp")
	require.NotNil(t, g)
	require.Len(t, g.Attributes, 1)
	attr := g.Attributes[0]
	assert.Equal(t, "server.address", attr.ID)
	assert.Equal(t, "Upstream host.", attr.Brief)
	assert.Equal(t, "string", attr.Type.Value)
}

func TestMerge_DoesNotMutateOriginals(t *testing.T) {
	t.Parallel()
	fsBase := fstest.MapFS{
		"server/registry.yaml": &fstest.MapFile{
			Data: []byte(`
groups:
  - id: registry.server
    type: attribute_group
    brief: 'Server attributes.'
    attributes:
      - id: server.address
        type: string
        brief: 'Server address.'
        stability: stable
`),
		},
	}
	fsUser := fstest.MapFS{
		"myapp/registry.yaml": &fstest.MapFile{
			Data: []byte(`
groups:
  - id: registry.myapp
    type: attribute_group
    brief: 'My app.'
    attributes:
      - ref: server.address
        brief: 'Upstream host.'
`),
		},
	}

	base, err := Load(fsBase)
	require.NoError(t, err)
	user, err := Load(fsUser)
	require.NoError(t, err)

	// Capture the original ref attribute state before merge.
	userAttr := user.Group("registry.myapp").Attributes[0]
	origType := userAttr.Type.Value

	_ = base.Merge(user)

	// The original user registry's attribute should be unchanged.
	afterAttr := user.Group("registry.myapp").Attributes[0]
	assert.Equal(t, origType, afterAttr.Type.Value, "Merge should not mutate the original registry")
}

func TestLoad_NonYAMLFilesIgnored(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"http/README.md": &fstest.MapFile{Data: []byte("# HTTP")},
		"http/registry.yaml": &fstest.MapFile{
			Data: []byte(`
groups:
  - id: registry.http
    type: attribute_group
    brief: 'HTTP.'
`),
		},
	}
	reg, err := Load(fsys)
	require.NoError(t, err)
	assert.Len(t, reg.Groups(), 1)
}
