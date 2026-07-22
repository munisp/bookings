package packs

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// SPEC-W3 §4 innovations 6 + 15: optional agents / customTools pack fields.

const packWithCrewAndTools = validPack + `
agents:
- id: intake-nurse
  name: Intake Nurse
  persona: You guide new patients through intake.
  intents: [intake, "new patient", form]
- id: insurance-checker
  name: Insurance Checker
  persona: You answer coverage and billing questions.
  intents: [insurance, coverage, billing]
customTools:
- name: check_calendar_availability
  description: Check open appointment slots
  method: GET
  url: http://booking:7002/public/sites/{{site_slug}}/availability
`

func TestLoadPackWithAgentsAndCustomTools(t *testing.T) {
	reg, err := Load(writePack(t, "crew.yaml", packWithCrewAndTools))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p, ok := reg.Get("test-pack")
	if !ok {
		t.Fatal("pack not found")
	}
	if len(p.Agents) != 2 || p.Agents[0].ID != "intake-nurse" || len(p.Agents[0].Intents) != 3 {
		t.Fatalf("agents parsed incorrectly: %+v", p.Agents)
	}
	if len(p.CustomTools) != 1 || p.CustomTools[0].Name != "check_calendar_availability" {
		t.Fatalf("customTools parsed incorrectly: %+v", p.CustomTools)
	}
	// Passthrough into the runtime Summary (camelCase JSON).
	s := p.Summary(nil)
	if len(s.Agents) != 2 || len(s.CustomTools) != 1 {
		t.Fatalf("summary must pass agents/customTools through: %+v", s)
	}
}

func mustValidate(t *testing.T, yamlBody string) error {
	t.Helper()
	var p Pack
	full := validPack + yamlBody
	if err := yaml.Unmarshal([]byte(full), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return p.Validate()
}

func TestValidateDuplicateAgentIDs(t *testing.T) {
	err := mustValidate(t, `
agents:
- {id: a, name: A, persona: p, intents: [x]}
- {id: a, name: B, persona: p, intents: [y]}
`)
	if err == nil || !strings.Contains(err.Error(), "duplicate agent id") {
		t.Fatalf("expected duplicate agent id error, got %v", err)
	}
}

func TestValidateAgentMissingFields(t *testing.T) {
	cases := []string{
		`agents: [{id: a, name: "", persona: p, intents: [x]}]`,
		`agents: [{id: a, name: A, persona: "", intents: [x]}]`,
		`agents: [{id: a, name: A, persona: p}]`,
		`agents: [{id: "BAD ID", name: A, persona: p, intents: [x]}]`,
		`agents: [{id: a, name: A, persona: p, intents: [" "]}]`,
	}
	for i, body := range cases {
		if err := mustValidate(t, "\n"+body+"\n"); err == nil {
			t.Fatalf("case %d: expected validation error", i)
		}
	}
}

func TestValidateCustomTools(t *testing.T) {
	bad := []string{
		`customTools: [{name: "bad name", method: GET, url: "http://booking:7002/x"}]`,
		`customTools: [{name: t, method: TRACE, url: "http://booking:7002/x"}]`,
		`customTools: [{name: t, method: GET, url: "not-a-url"}]`,
		`customTools: [{name: t, method: GET, url: "ftp://booking/x"}]`,
		`customTools: [{name: t, method: GET, url: "http://booking:7002/x"}, {name: t, method: POST, url: "http://booking:7002/y"}]`,
	}
	for i, body := range bad {
		if err := mustValidate(t, "\n"+body+"\n"); err == nil {
			t.Fatalf("case %d: expected validation error", i)
		}
	}
	good := `customTools: [{name: check, method: POST, url: "http://booking:7002/x", bodyTemplate: "{\"a\": \"{{a}}\"}"}]`
	if err := mustValidate(t, "\n"+good+"\n"); err != nil {
		t.Fatalf("valid customTools rejected: %v", err)
	}
}

func TestPacksWithoutNewFieldsStillValidate(t *testing.T) {
	if err := mustValidate(t, ""); err != nil {
		t.Fatalf("pack without agents/customTools must validate: %v", err)
	}
}
