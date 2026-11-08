package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the page's JSON contract so the regression also runs on code
// from before marker configuration existed.
func saveMarkerJSON(t *testing.T, text string) *Result {
	t.Helper()
	var c InstructionsChange
	if err := json.Unmarshal([]byte(text), &c); err != nil {
		t.Fatal(err)
	}
	return ok(t)(SaveInstructions(c))
}

func TestInstructionMarkersMigrateAndRemove(t *testing.T) {
	h := sandbox(t)
	p := filepath.Join(h, ".codex/AGENTS.md")
	write(t, p, "Personal rules.\n")
	saveMarkerJSON(t, `{"shared":"Shared rules.","agents":["codex"],"extra":{"codex":"Codex rules."}}`)
	saveMarkerJSON(t, `{"markers":{"begin":"<!-- team:begin -->","end":"<!-- team:end -->"}}`)
	want := "Personal rules.\n\n<!-- team:begin -->\nShared rules.\n\nCodex rules.\n<!-- team:end -->\n"
	if got := read(t, p); got != want {
		t.Fatalf("custom markers did not replace the default block:\n%s", got)
	}
	if res := ok(t)(Sync()); len(res.Changed) != 0 {
		t.Fatalf("sync rewrote an unchanged block: %v", res.Changed)
	}
	saveMarkerJSON(t, `{"shared":"Updated shared rules.","markers":{"begin":"<!-- work:start -->","end":"<!-- work:stop -->"}}`)
	got := read(t, p)
	if strings.Contains(got, "team:") || strings.Count(got, "<!-- work:start -->") != 1 || !strings.Contains(got, "Updated shared rules.\n\nCodex rules.") {
		t.Fatalf("a second marker change left an old block or lost text:\n%s", got)
	}
	saveMarkerJSON(t, `{"markers":{}}`)
	got = read(t, p)
	if strings.Contains(got, "work:") || strings.Count(got, blockBegin) != 1 {
		t.Fatalf("restoring the default markers left the old block:\n%s", got)
	}
	saveMarkerJSON(t, `{"agents":[]}`)
	if got := read(t, p); got != "Personal rules.\n" {
		t.Fatalf("turning instructions off changed personal rules: %q", got)
	}
}

func TestInstructionMarkersKeepManualEdits(t *testing.T) {
	h := sandbox(t)
	p := filepath.Join(h, ".codex/AGENTS.md")
	saveMarkerJSON(t, `{"shared":"Shared rules.","agents":["codex"]}`)
	write(t, p, strings.ReplaceAll(read(t, p), "Shared rules.", "My edited rules."))
	saveMarkerJSON(t, `{"markers":{"begin":"<!-- team:begin -->","end":"<!-- team:end -->"}}`)
	if got := read(t, p); !strings.Contains(got, "My edited rules.") || !strings.Contains(got, "team:begin") {
		t.Fatalf("changing markers discarded a manual edit:\n%s", got)
	}
	ok(t)(Sync())
	if got := read(t, p); !strings.Contains(got, "My edited rules.") {
		t.Fatalf("the next sync discarded a manual edit:\n%s", got)
	}
	saveMarkerJSON(t, `{"shared":"New shared rules."}`)
	if got := read(t, p); strings.Contains(got, "My edited") || !strings.Contains(got, "New shared rules.") {
		t.Fatalf("a library edit did not update the block:\n%s", got)
	}
}

func TestInstructionMarkersInvalidChangeWritesNothing(t *testing.T) {
	h := sandbox(t)
	p := filepath.Join(h, ".codex/AGENTS.md")
	saveMarkerJSON(t, `{"shared":"Original.","agents":["codex"]}`)
	before := read(t, p)
	for _, markers := range []string{
		`{"begin":"","end":"<!-- end -->"}`,
		`{"begin":"<!-- same -->","end":"<!-- same -->"}`,
		`{"begin":"<!-- begin -->\nOther text","end":"<!-- end -->"}`,
		`{"begin":"<!-- broken -- comment -->","end":"<!-- end -->"}`,
		`{"begin":"not a comment","end":"<!-- end -->"}`,
	} {
		var c InstructionsChange
		if err := json.Unmarshal([]byte(`{"shared":"Should not be written.","markers":`+markers+`}`), &c); err != nil {
			t.Fatal(err)
		}
		if _, err := SaveInstructions(c); err == nil {
			t.Fatalf("accepted invalid markers: %s", markers)
		}
		if got := read(t, p); got != before {
			t.Fatalf("invalid markers changed the agent's file: %q", got)
		}
	}
}

func TestInstructionMarkersCarryAndImport(t *testing.T) {
	h := sandbox(t)
	p := filepath.Join(h, ".codex/AGENTS.md")
	write(t, p, "Personal rules.\n")
	saveMarkerJSON(t, `{"shared":"Shared rules.","agents":["codex"],"markers":{"begin":"<!-- team:begin -->","end":"<!-- team:end -->"}}`)
	ok(t)(ImportInstructions("codex"))
	if got := read(t, p); strings.Count(got, "Personal rules.") != 1 || strings.Count(got, "team:begin") != 1 {
		t.Fatalf("import duplicated the block or personal rules:\n%s", got)
	}
	b, err := Collect()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mustJSON(t, b), `team:begin`) {
		t.Fatal("backup lost the configured markers")
	}
	h2 := sandbox(t)
	p2 := filepath.Join(h2, ".codex/AGENTS.md")
	write(t, p2, "Other machine rules.\n")
	ok(t)(Put(b))
	if got := read(t, p2); !strings.Contains(got, "team:begin") || !strings.Contains(got, "Other machine rules.") {
		t.Fatalf("restore lost markers or personal rules:\n%s", got)
	}
	saveMarkerJSON(t, `{"agents":[]}`)
	if _, err := os.Stat(p2); err != nil {
		t.Fatal("removing the custom block removed the personal file:", err)
	}
}

func TestInstructionMarkersRefusePersonalCollision(t *testing.T) {
	h := sandbox(t)
	p := filepath.Join(h, ".codex/AGENTS.md")
	personal := "<!-- team:begin -->\nPersonal rules.\n<!-- team:end -->\n"
	write(t, p, personal)
	saveMarkerJSON(t, `{"shared":"Shared rules.","agents":["codex"]}`)
	before := read(t, p)
	var c InstructionsChange
	json.Unmarshal([]byte(`{"markers":{"begin":"<!-- team:begin -->","end":"<!-- team:end -->"}}`), &c)
	if _, err := SaveInstructions(c); err == nil {
		t.Fatal("accepted markers colliding with personal text")
	}
	if got := read(t, p); got != before {
		t.Fatal("a refused marker change changed the personal file")
	}
	b, err := Collect()
	if err != nil {
		t.Fatal(err)
	}
	b.Instructions.Markers = c.Markers
	if _, err := Put(b); err == nil {
		t.Fatal("backup restore accepted a personal marker collision")
	}
	if got := read(t, p); got != before {
		t.Fatal("backup restore overwrote personal text")
	}
	// A stopped agent cannot be checked at save time. Check again at the
	// write, just as when that agent next becomes reachable.
	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	l.Instructions.Markers = c.Markers
	res := &Result{}
	l.syncInstructions(targetByID("codex"), newBackups(), res)
	if len(res.Problems) != 1 || read(t, p) != before {
		t.Fatalf("late sync did not protect personal text: %+v", res)
	}
}

func TestInstructionMarkersRefuseCollisionInManualEdit(t *testing.T) {
	h := sandbox(t)
	p := filepath.Join(h, ".codex/AGENTS.md")
	saveMarkerJSON(t, `{"shared":"Shared rules.","agents":["codex"]}`)
	before := strings.ReplaceAll(read(t, p), "Shared rules.", "My rules.\n<!-- team:end -->\nMore rules.")
	write(t, p, before)
	var c InstructionsChange
	json.Unmarshal([]byte(`{"markers":{"begin":"<!-- team:begin -->","end":"<!-- team:end -->"}}`), &c)
	res, err := SaveInstructions(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Problems) != 1 || read(t, p) != before {
		t.Fatalf("a manual marker collision damaged the file: %+v", res)
	}
}

func TestInstructionMarkersCheckFinalText(t *testing.T) {
	h := sandbox(t)
	p := filepath.Join(h, ".codex/AGENTS.md")
	saveMarkerJSON(t, `{"shared":"Shared rules.","agents":["codex"],"extra":{"codex":"Example: <!-- team:begin -->"}}`)
	res := saveMarkerJSON(t, `{"markers":{"begin":"<!-- team:begin -->","end":"<!-- team:end -->"},"extra":{"codex":"Safe rules."}}`)
	if len(res.Problems) != 0 || !strings.Contains(read(t, p), "Safe rules.") {
		t.Fatalf("the previous extra text blocked a safe replacement: %+v", res)
	}
}

func TestInstructionMarkersSetupSnapshotAndRestore(t *testing.T) {
	h := sandbox(t)
	p := filepath.Join(h, ".codex/AGENTS.md")
	saveMarkerJSON(t, `{"shared":"Shared rules.","agents":["codex"],"markers":{"begin":"<!-- team:begin -->","end":"<!-- team:end -->"}}`)
	s, err := Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !Holds(s) {
		t.Fatal("the current setup does not hold its snapshot")
	}
	saveMarkerJSON(t, `{"markers":{}}`)
	if Holds(s) {
		t.Fatal("changing markers did not make the snapshot differ")
	}
	ok(t)(Restore(s))
	if !Holds(s) || !strings.Contains(read(t, p), "team:begin") {
		t.Fatal("restoring the setup did not restore markers")
	}
	// Simulate a source that resolves to the agent's own file, without
	// needing Windows symlink privileges. Generated text is not source text.
	write(t, p, "Personal rules.\n\n"+read(t, p))
	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	l.Instructions.Sets = []InstrSet{{ID: defaultSet, File: p}}
	if err := l.save(); err != nil {
		t.Fatal(err)
	}
	s, err = Snapshot()
	if err != nil || s.Instructions.Shared != "Personal rules." {
		t.Fatalf("snapshot included generated text: %+v %v", s, err)
	}
}

func TestInstructionMarkersKeepManualEmptyBlock(t *testing.T) {
	h := sandbox(t)
	p := filepath.Join(h, ".codex/AGENTS.md")
	saveMarkerJSON(t, `{"shared":"Shared rules.","agents":["codex"]}`)
	write(t, p, blockBegin+"\n"+blockEnd+"\n")
	saveMarkerJSON(t, `{"markers":{"begin":"<!--team:begin-->","end":"<!--team:end-->"}}`)
	want := "<!--team:begin-->\n<!--team:end-->\n"
	if got := read(t, p); got != want {
		t.Fatalf("changing markers lost a manually emptied block: %q", got)
	}
	ok(t)(Sync())
	if got := read(t, p); got != want {
		t.Fatalf("sync restored text over a manually emptied block: %q", got)
	}
	saveMarkerJSON(t, `{"agents":[]}`)
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("turning instructions off kept the empty managed block: %v", err)
	}
}

func TestInstructionMarkersImportAfterDeferredMigration(t *testing.T) {
	h := sandbox(t)
	p := filepath.Join(h, ".codex/AGENTS.md")
	write(t, p, "Personal rules.\n")
	saveMarkerJSON(t, `{"shared":"Shared rules.","agents":["codex"],"markers":{"begin":"<!-- old:begin -->","end":"<!-- old:end -->"}}`)
	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	l.Instructions.Markers = &InstructionMarkers{Begin: "<!-- new:begin -->", End: "<!-- new:end -->"}
	if err := l.save(); err != nil {
		t.Fatal(err)
	}
	ok(t)(ImportInstructions("codex"))
	got := read(t, p)
	if strings.Contains(got, "old:") || strings.Count(got, "new:begin") != 1 || strings.Count(got, "Personal rules.") != 1 {
		t.Fatalf("import after a deferred migration did not write the shared block:\n%s", got)
	}
}
