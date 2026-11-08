package library

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/edit"
)

// The part of an agent's instructions file magpie writes sits between
// these two lines; the rest of the file is the user's.
const (
	blockBegin = "<!-- magpie:begin · written by magpie from its Library -->"
	blockEnd   = "<!-- magpie:end -->"
)

// defaultSet is the set magpie had before there were more: its text is
// where the one shared text always was.
const defaultSet = "default"

// setPath is where a set's text is kept.
func setPath(id string) string {
	if id == "" || id == defaultSet {
		return filepath.Join(Dir(), "instructions.md")
	}
	return filepath.Join(Dir(), "instruction-sets", id+".md")
}

// sets are the sets there are, the default first.
func (l *Library) sets() []InstrSet {
	out := []InstrSet{{ID: defaultSet}}
	for _, s := range l.Instructions.Sets {
		if s.ID == defaultSet {
			out[0] = s
		} else {
			out = append(out, s)
		}
	}
	return out
}

func (l *Library) instrSet(id string) *InstrSet {
	for i := range l.Instructions.Sets {
		if l.Instructions.Sets[i].ID == id {
			return &l.Instructions.Sets[i]
		}
	}
	return nil
}

func (l *Library) hasSet(id string) bool { return id == defaultSet || l.instrSet(id) != nil }

// active is the set the agents get.
func (l *Library) active() string {
	if l.hasSet(l.Instructions.Active) {
		return l.Instructions.Active
	}
	return defaultSet
}

// sharedPath is where the text the agents get is kept.
func (l *Library) sharedPath() string { return l.textPath(l.active()) }

// textPath is where a set's text is kept: the user's own file, for a set
// linked to one, or setPath.
func (l *Library) textPath(id string) string {
	if s := l.instrSet(id); s != nil && s.File != "" {
		return s.File
	}
	return setPath(id)
}

// linked reports whether p is a file of the user's a set is linked to.
func (l *Library) linked(p string) bool {
	return slices.ContainsFunc(l.Instructions.Sets, func(s InstrSet) bool { return s.File != "" && s.File == p })
}

// putText writes a set's or an agent's text to p. A file of the user's a
// set is linked to is kept aside first and written, never removed: an
// emptied set leaves it empty, and a text it has already is no write.
func (l *Library) putText(p, text string) error {
	if !l.linked(p) {
		return writeText(p, text)
	}
	text = strings.TrimSpace(text)
	if readText(p) == text {
		return nil
	}
	if l.kept == nil {
		l.kept = newBackups()
	}
	if err := l.kept.keep("library", p); err != nil {
		return err
	}
	if text != "" {
		text += "\n"
	}
	return edit.WriteAtomic(p, []byte(text))
}

func extraPath(agent string) string {
	return filepath.Join(Dir(), "instructions", fileName(agent)+".md")
}

// isSetFile reports whether p, links followed, is the file the agents' set
// is kept in.
func (l *Library) isSetFile(p string) bool {
	return p != "" && realBelow(p) == realBelow(l.sharedPath())
}

func readText(path string) string {
	b, _ := os.ReadFile(path)
	return strings.TrimSpace(strings.ReplaceAll(string(b), "\r\n", "\n"))
}

func writeText(path, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		if err := edit.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return edit.WriteAtomic(path, []byte(text+"\n"))
}

// wanted is what magpie writes into the agent's file: the shared text and
// the agent's own additions after it; "" for nothing.
func (l *Library) wanted(agent string) string {
	if !slices.Contains(l.Instructions.Agents, agent) {
		return ""
	}
	parts := []string{}
	for _, s := range []string{l.setText(l.sharedPath()), readText(extraPath(agent))} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n")
}

// split takes a file apart: what is before magpie's part, magpie's part,
// and what is after it. ok is false when magpie has no part in it.
func split(file string, markers ...InstructionMarkers) (before, block, after string, ok bool) {
	first := len(file)
	before = file
	for _, m := range markerPairs(markers) {
		i := strings.Index(file, m.Begin)
		if i < 0 || i >= first {
			continue
		}
		rest := file[i+len(m.Begin):]
		j := strings.Index(rest, m.End)
		if j < 0 {
			continue
		}
		first = i
		before, block, after, ok = file[:i], strings.TrimSpace(rest[:j]), rest[j+len(m.End):], true
	}
	return
}

// own is the file without magpie's part: the user's own instructions.
// Every part goes, and an end line left over: a part written into itself
// (#2349) holds others, each ended after the first end.
func own(file string, markers ...InstructionMarkers) string {
	for {
		before, _, after, ok := split(file, markers...)
		if !ok {
			break
		}
		file = strings.TrimSpace(before) + "\n\n" + strings.TrimSpace(after)
	}
	for _, m := range markerPairs(markers) {
		if strings.Contains(file, m.End) {
			lines := strings.Split(file, "\n")
			lines = slices.DeleteFunc(lines, func(s string) bool { return strings.TrimSpace(s) == m.End })
			file = strings.Join(lines, "\n")
			for strings.Contains(file, "\n\n\n") {
				file = strings.ReplaceAll(file, "\n\n\n", "\n\n")
			}
		}
	}
	return strings.TrimSpace(file)
}

// withBlock is the file with magpie's part set to text, or taken out for "".
// A new part goes after what the user has written.
func withBlock(file, text string, markers ...InstructionMarkers) string {
	markers = markerPairs(markers)
	block := ""
	if text != "" {
		block = markers[0].Begin + "\n" + text + "\n" + markers[0].End
	}
	return replaceBlock(file, block, markers...)
}

// replaceBlock accepts a whole block, including an intentionally empty one
// whose marker lines preserve a user's edit. An empty block string removes it.
func replaceBlock(file, block string, markers ...InstructionMarkers) string {
	file = strings.ReplaceAll(file, "\r\n", "\n")
	before, _, after, ok := split(file, markers...)
	before, after = strings.TrimRight(before, "\n"), strings.TrimLeft(after, "\n")
	if !ok {
		before, after = strings.TrimRight(file, "\n"), ""
	}
	var parts []string
	for _, p := range []string{before, block, strings.TrimRight(after, "\n")} {
		if strings.TrimSpace(p) != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n") + "\n"
}

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

// syncInstructions writes the agent's part as the library has it. A part
// the user edited in the file is left alone until the library's text for
// that agent changes; then it is replaced, the file kept aside first.
func (l *Library) syncInstructions(t *Target, b *backups, res *Result) {
	if res.unreached(t) {
		return
	}
	if t.Instructions == "" {
		return
	}
	id := t.Agent.ID
	a := l.applied(id)
	want := l.wanted(id)
	raw, err := edit.Read(t.Instructions)
	if err != nil {
		res.fail(id, "instructions", err)
		return
	}
	file := string(raw)
	previous := l.previousMarkers(id)
	written := l.Instructions.Markers.effective()
	markers := append([]InstructionMarkers{written}, previous...)
	sameMarkers := a.InstrMarkers.effective() == written
	if l.isSetFile(t.Instructions) {
		// the agent reads the set's own file, so it has the text already:
		// magpie writes no part into it, and takes out one written before
		file = strings.ReplaceAll(file, "\r\n", "\n")
		if own(file, previous...) == strings.TrimSpace(file) {
			a.Instructions, a.InstrHash, a.InstrMarkers = false, "", nil
			return
		}
		if err := b.keep(id, t.Instructions); err != nil {
			res.fail(id, "instructions", err)
			return
		}
		next := own(file, previous...)
		if next != "" {
			next += "\n"
		}
		if err := edit.WriteAtomic(t.Instructions, []byte(next)); err != nil {
			res.fail(id, "instructions", err)
			return
		}
		a.Instructions, a.InstrHash, a.InstrMarkers = false, "", nil
		res.changed(id)
		return
	}
	_, have, _, ok := split(file, previous...)
	preserveEmpty := false
	if want != "" {
		if err := l.checkPersonalMarkers(id, file, written); err != nil {
			res.fail(id, "instructions", err)
			return
		}
	}
	if want == "" && !ok {
		a.Instructions, a.InstrHash = false, ""
		a.InstrMarkers = nil
		return
	}
	if ok && have == want && sameMarkers && want != "" {
		a.Instructions, a.InstrHash = want != "", hash(want)
		return
	}
	if ok && want != "" && a.InstrHash != "" && hash(have) != a.InstrHash && hash(want) == a.InstrHash {
		if sameMarkers {
			return // edited in the file since, and the library hasn't changed for it
		}
		// Changing only the markers preserves edits made in the agent's file.
		want = have
		preserveEmpty = have == ""
	}
	if err := written.checkText(want); err != nil {
		res.fail(id, "instructions", err)
		return
	}
	if want == "" && !preserveEmpty {
		markers = previous
	}
	next := withBlock(file, want, markers...)
	if preserveEmpty {
		next = replaceBlock(file, written.Begin+"\n"+written.End, markers...)
	}
	if err := b.keep(id, t.Instructions); err != nil {
		res.fail(id, "instructions", err)
		return
	}
	if strings.TrimSpace(next) == "" && len(raw) > 0 && own(file, markers...) == "" {
		err = edit.Remove(t.Instructions)
	} else if next != "" {
		err = edit.WriteAtomic(t.Instructions, []byte(next))
	}
	if err != nil {
		res.fail(id, "instructions", err)
		return
	}
	a.Instructions, a.InstrHash = l.wanted(id) != "", hash(l.wanted(id))
	a.InstrMarkers = nil
	if a.Instructions && written != defaultMarkers() {
		a.InstrMarkers = &written
	}
	res.changed(id)
}

// AgentInstructions is one agent's instructions file as the page shows it.
type AgentInstructions struct {
	Agent    string `json:"agent"`
	Name     string `json:"name"`
	Icon     string `json:"icon"`
	Path     string `json:"path"`
	On       bool   `json:"on"`
	Extra    string `json:"extra"`              // what this agent gets besides
	Own      int    `json:"own"`                // lines of the user's own in the file
	Edited   bool   `json:"edited,omitempty"`   // magpie's part was changed in the file
	Override string `json:"override,omitempty"` // a file the agent reads instead
	Profile  bool   `json:"profile,omitempty"`  // the file is the user's profile to the agent (Target.Profile)
	// Asleep is the WSL distro, not running now, the agent is in: its row
	// is what magpie last knew of it (asleepInstructions), its file isn't
	// read, and a change is written there when the distro next runs.
	// Unseen: magpie never saw where it keeps its instructions
	Asleep string `json:"asleep,omitempty"`
	Unseen bool   `json:"unseen,omitempty"`
}

// InstructionsView is the Instructions page.
type InstructionsView struct {
	Markers InstructionMarkers  `json:"markers"`
	Shared  string              `json:"shared"` // the set on's text
	Sets    []SetView           `json:"sets"`
	Agents  []AgentInstructions `json:"agents"`
}

// SetView is one set of shared instructions as the page shows it.
type SetView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Text   string `json:"text"`
	Active bool   `json:"active,omitempty"`
	File   string `json:"file,omitempty"` // the user's own file it is kept in
}

// ReadInstructions is the shared text and each agent's file.
func ReadInstructions() (*InstructionsView, error) {
	mu.Lock()
	defer mu.Unlock()
	l, err := load()
	if err != nil {
		return nil, err
	}
	return l.instructionsView(targetsAsleep(false)), nil
}

// instructionsView is the Instructions page for these agents, and those in
// stopped WSL distros (asleep). They are passed in: finding them looks
// through every agent's folders, in WSL too, which one read of the Library
// did three times over (#1624).
func (l *Library) instructionsView(targets []*Target, asleep []*agent.Agent) *InstructionsView {
	v := &InstructionsView{Shared: l.setText(l.sharedPath()), Markers: l.Instructions.Markers.effective(), Agents: []AgentInstructions{}}
	for _, s := range l.sets() {
		v.Sets = append(v.Sets, SetView{ID: s.ID, Name: s.Name, Text: l.setText(l.textPath(s.ID)), Active: s.ID == l.active(), File: s.File})
	}
	for _, t := range targets {
		if t.Instructions == "" {
			continue
		}
		id := t.Agent.ID
		ai := AgentInstructions{Agent: id, Name: t.Agent.Name, Icon: t.Agent.Icon, Path: t.Instructions,
			On: slices.Contains(l.Instructions.Agents, id), Extra: readText(extraPath(id)), Profile: t.Profile}
		file := readText(t.Instructions)
		if o := own(file, l.previousMarkers(id)...); o != "" {
			ai.Own = strings.Count(o, "\n") + 1
		}
		if _, have, _, ok := split(file, l.previousMarkers(id)...); ok && have != l.wanted(id) && !l.isSetFile(t.Instructions) {
			ai.Edited = true
		}
		if t.Override != "" && readText(t.Override) != "" {
			ai.Override = t.Override
		}
		v.Agents = append(v.Agents, ai)
	}
	// a stopped WSL distro's agents, by what magpie last knew of them:
	// nothing of theirs is opened, which would start the distro (#3896)
	for _, a := range asleep {
		if ai, ok := l.asleepInstructions(a); ok {
			v.Agents = append(v.Agents, ai)
		}
	}
	return v
}

// InstructionsChange is what the page saves: the shared text, which agents
// get it, and what each gets besides. A nil field is left as it is.
type InstructionsChange struct {
	Markers *InstructionMarkers `json:"markers,omitempty"`
	Shared  *string             `json:"shared,omitempty"`
	Agents  []string            `json:"agents,omitempty"`
	Extra   map[string]*string  `json:"extra,omitempty"`
	// Rewrite writes magpie's part again into these agents' files, over
	// edits made there.
	Rewrite []string `json:"rewrite,omitempty"`
	// The sets: texts by set, a set to switch the agents to, one to add
	// (id and name), one to rename and one to remove. Shared is the text
	// of the set on, as it is after Activate.
	Texts    map[string]*string `json:"texts,omitempty"`
	Activate string             `json:"activate,omitempty"`
	Create   *InstrSet          `json:"create,omitempty"`
	Rename   *InstrSet          `json:"rename,omitempty"`
	Remove   string             `json:"remove,omitempty"`
}

// changeInstructions is change for the instructions alone: it writes them
// into the agents and nothing else. Through change, a set made, saved or
// removed gave every agent its MCP servers and skills again too, looking
// through every skill folder, which took up to a minute a click on Windows
// (#1624, Hipple).
func changeInstructions(f func(l *Library) error) (*Result, error) {
	mu.Lock()
	defer mu.Unlock()
	l, err := load()
	if err != nil {
		return nil, err
	}
	if err := f(l); err != nil {
		return nil, err
	}
	if err := l.save(); err != nil {
		return nil, err
	}
	res := &Result{Changed: []string{}, Problems: []Problem{}}
	b := l.kept
	if b == nil {
		b = newBackups()
	}
	for _, t := range Targets() {
		l.syncInstructions(t, b, res)
	}
	res.Backup = b.dir
	if b.dir != "" {
		pruneBackups()
	}
	return res, l.save()
}

// SaveInstructions saves the change and writes it into the agents.
func SaveInstructions(c InstructionsChange) (*Result, error) {
	if err := c.Markers.check(); err != nil {
		return nil, err
	}
	return changeInstructions(func(l *Library) error {
		if err := l.checkMarkerChange(c); err != nil {
			return err
		}
		if c.Markers != nil {
			m := c.Markers.effective()
			l.Instructions.Markers = nil
			if m != defaultMarkers() {
				l.Instructions.Markers = &m
			}
		}
		if err := l.changeSets(c); err != nil {
			return err
		}
		if c.Shared != nil {
			if err := l.putText(l.sharedPath(), *c.Shared); err != nil {
				return err
			}
		}
		if c.Agents != nil {
			l.Instructions.Agents = slices.Sorted(slices.Values(c.Agents))
		}
		for id, x := range c.Extra {
			if x == nil {
				continue
			}
			if err := checkAgent(id); err != nil {
				return err
			}
			if err := writeText(extraPath(id), *x); err != nil {
				return err
			}
		}
		for _, id := range c.Rewrite {
			l.applied(id).InstrHash = ""
		}
		return nil
	})
}

// ImportInstructions moves the user's own instructions out of an agent's
// file into the library: they are added to the shared text, taken out of
// the file (which is kept aside first), and the agent gets the shared text
// from then on — so what it reads is what it read before.
func ImportInstructions(id string) (*Result, error) {
	t := targetByID(id)
	if t == nil || t.Instructions == "" {
		return nil, fmt.Errorf("%s has no instructions file magpie knows", id)
	}
	if t.Profile {
		// moved out, the profile would go into every agent's file, and the
		// agent would lose who its owner is (Alma's USER.md, #2315)
		return nil, fmt.Errorf("%s is %s's profile of its owner, not instructions alone: what is in it stays there", t.Instructions, t.Agent.Name)
	}
	return changeInstructions(func(l *Library) error {
		if l.isSetFile(t.Instructions) {
			return fmt.Errorf("%s is the file the instructions set is kept in already", t.Instructions)
		}
		raw, err := edit.Read(t.Instructions)
		if err != nil {
			return err
		}
		file := strings.ReplaceAll(string(raw), "\r\n", "\n")
		mine := own(file, l.previousMarkers(id)...)
		if mine == "" {
			return fmt.Errorf("%s has nothing of its own to import", t.Instructions)
		}
		shared := l.setText(l.sharedPath())
		if !strings.Contains(shared, mine) {
			shared = strings.TrimSpace(shared + "\n\n" + mine)
		}
		if err := l.Instructions.Markers.effective().checkText(shared); err != nil {
			return err
		}
		if err := l.putText(l.sharedPath(), shared); err != nil {
			return err
		}
		b := newBackups()
		if err := b.keep(id, t.Instructions); err != nil {
			return err
		}
		_, block, _, ok := split(file, l.previousMarkers(id)...)
		next := ""
		if ok {
			next = withBlock("", block, l.previousMarkers(id)...)
		}
		if next == "" {
			err = edit.Remove(t.Instructions)
		} else {
			err = edit.WriteAtomic(t.Instructions, []byte(next))
		}
		if err != nil {
			return err
		}
		l.Instructions.Agents = set(l.Instructions.Agents, id, true)
		l.applied(id).InstrHash = "" // what's there now isn't what the library says
		return nil
	})
}

// changeSets adds, renames, fills, removes and switches the sets as c says.
func (l *Library) changeSets(c InstructionsChange) error {
	if x := c.Create; x != nil {
		if err := checkName("set", x.ID); err != nil {
			return err
		}
		if l.hasSet(x.ID) {
			return fmt.Errorf("there is a set %q already", x.ID)
		}
		if strings.TrimSpace(x.Name) == "" {
			return errors.New("a new set needs a name")
		}
		file := ""
		if x.File != "" {
			var err error
			if file, err = l.linkable(x.File); err != nil {
				return err
			}
		}
		l.Instructions.Sets = append(l.Instructions.Sets, InstrSet{ID: x.ID, Name: strings.TrimSpace(x.Name), File: file})
	}
	if x := c.Rename; x != nil {
		if !l.hasSet(x.ID) {
			return fmt.Errorf("there is no set %q", x.ID)
		}
		if s := l.instrSet(x.ID); s != nil {
			s.Name = strings.TrimSpace(x.Name)
		} else {
			l.Instructions.Sets = append([]InstrSet{{ID: x.ID, Name: strings.TrimSpace(x.Name)}}, l.Instructions.Sets...)
		}
	}
	for id, text := range c.Texts {
		if text == nil {
			continue
		}
		if !l.hasSet(id) {
			return fmt.Errorf("there is no set %q", id)
		}
		if err := l.putText(l.textPath(id), *text); err != nil {
			return err
		}
	}
	if id := c.Remove; id != "" {
		switch {
		case id == defaultSet:
			return errors.New("the first set can be renamed and emptied, not removed")
		case !l.hasSet(id):
			return fmt.Errorf("there is no set %q", id)
		case id == l.active():
			return errors.New("the set the agents get can't be removed: switch them to another first")
		}
		// a linked file is the user's: the set lets go of it, and it stays
		if l.instrSet(id).File == "" {
			if err := writeText(setPath(id), ""); err != nil {
				return err
			}
		}
		l.Instructions.Sets = slices.DeleteFunc(l.Instructions.Sets, func(s InstrSet) bool { return s.ID == id })
	}
	// an empty set in use gives the agents nothing, so a set added, or one
	// given text, is the one they read from then on (Fate on Discord: a new
	// set was written into no agent while the Default set read was empty)
	if c.Activate == "" && c.Shared == nil && l.setText(l.sharedPath()) == "" {
		if x := c.Create; x != nil {
			l.Instructions.Active = x.ID
		}
		for _, id := range slices.Sorted(maps.Keys(c.Texts)) {
			if x := c.Texts[id]; x != nil && strings.TrimSpace(*x) != "" && id != l.active() {
				l.Instructions.Active = id
				break
			}
		}
		if l.Instructions.Active == defaultSet {
			l.Instructions.Active = ""
		}
	}
	if id := c.Activate; id != "" {
		if !l.hasSet(id) {
			return fmt.Errorf("there is no set %q", id)
		}
		l.Instructions.Active = id
		if id == defaultSet {
			l.Instructions.Active = ""
		}
	}
	return nil
}

// linkable is the file a new set can be linked to, made absolute, or why
// not: a file magpie writes into (an agent's own instructions, where the
// set would be read from the text written from it), one of the library's
// own, a folder, or one another set has.
func (l *Library) linkable(file string) (string, error) {
	file = strings.TrimSpace(file)
	if rest, ok := strings.CutPrefix(file, "~"); ok && (rest == "" || rest[0] == '/' || rest[0] == filepath.Separator) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		file = home + rest
	}
	if !filepath.IsAbs(file) {
		return "", fmt.Errorf("%s: give the file's full path", file)
	}
	file = filepath.Clean(file)
	real := realBelow(file)
	if st, err := os.Stat(file); err == nil && st.IsDir() {
		return "", fmt.Errorf("%s is a folder: give a file", file)
	}
	if l.hasInstructionMarker(readText(file)) {
		return "", fmt.Errorf("magpie writes its instructions into %s: a set can't be kept there too", file)
	}
	if lib := realBelow(Dir()); real == lib || strings.HasPrefix(real, lib+string(filepath.Separator)) || real == realBelow(path()) {
		return "", fmt.Errorf("%s is the library's own", file)
	}
	for _, s := range l.Instructions.Sets {
		if s.File != "" && realBelow(s.File) == real {
			return "", fmt.Errorf("the set %q is kept in %s already", s.Name, file)
		}
	}
	for _, t := range everyTarget() {
		for _, p := range []string{t.Instructions, t.Override} {
			if p != "" && realBelow(p) == real {
				return "", fmt.Errorf("%s is %s's own instructions, which magpie writes into: a set can't be kept there", file, t.Agent.Name)
			}
		}
	}
	return file, nil
}

// linkSeen is the linked file SyncLinked last looked at, as it was then.
var linkSeen struct {
	sync.Mutex
	file string
	mod  time.Time
	size int64
}

// SyncLinked writes the instructions into the agents again when the set
// they get is kept in a file of the user's that changed since it was last
// looked at — edited by hand, or by another tool that keeps it — and not
// otherwise: nil, nil. The first look at a file only notes it.
func SyncLinked() (*Result, error) {
	mu.Lock()
	l, err := load()
	file := ""
	if err == nil {
		if s := l.instrSet(l.active()); s != nil {
			file = s.File
		}
	}
	mu.Unlock()
	if err != nil {
		return nil, err
	}
	var mod time.Time
	var size int64 = -1
	if st, err := os.Stat(file); file != "" && err == nil {
		mod, size = st.ModTime(), st.Size()
	}
	linkSeen.Lock()
	changed := file != "" && linkSeen.file == file && (!linkSeen.mod.Equal(mod) || linkSeen.size != size)
	linkSeen.file, linkSeen.mod, linkSeen.size = file, mod, size
	linkSeen.Unlock()
	if !changed {
		return nil, nil
	}
	return changeInstructions(func(*Library) error { return nil })
}

// KeepLinked runs SyncLinked, and Woken, every interval, until ctx ends:
// the app's backend, which ends once it has handed the gateway to the
// new version (gui/handover.go). Before, it ran for as long as the
// process did, and the copy an update left finishing its requests went
// on writing the agents' files beside the new one (#4938).
func KeepLinked(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for ctx.Err() == nil {
		if res, err := SyncLinked(); err != nil {
			log.Println("library: linked instructions:", err)
		} else if res != nil {
			for _, p := range res.Problems {
				log.Println("library: linked instructions:", p.Agent, p.What, p.Error)
			}
		}
		// what was ticked for a stopped WSL distro's agents, once it runs
		keepWoken()
		select {
		case <-ctx.Done():
		case <-t.C:
		}
	}
}
