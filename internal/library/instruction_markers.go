package library

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/edit"
)

// InstructionMarkers are the two HTML comment lines around the library's text.
// Applied keeps the previous pair until each agent has received a new one.
type InstructionMarkers struct {
	Begin string `json:"begin"`
	End   string `json:"end"`
}

func defaultMarkers() InstructionMarkers {
	return InstructionMarkers{Begin: blockBegin, End: blockEnd}
}

func (m *InstructionMarkers) effective() InstructionMarkers {
	if m == nil || *m == (InstructionMarkers{}) {
		return defaultMarkers()
	}
	return *m
}

func (m *InstructionMarkers) check() error {
	if m == nil || *m == (InstructionMarkers{}) {
		return nil
	}
	for _, line := range []string{m.Begin, m.End} {
		if len(line) > 512 || strings.ContainsAny(line, "\r\n") || !strings.HasPrefix(line, "<!--") || !strings.HasSuffix(line, "-->") {
			return errors.New("instruction markers must be single HTML comment lines, up to 512 bytes each")
		}
		body := strings.TrimSuffix(strings.TrimPrefix(line, "<!--"), "-->")
		if strings.TrimSpace(body) == "" || strings.Contains(body, "--") || strings.ContainsAny(body, "<>") {
			return errors.New("instruction markers need nonempty comment text without --, < or >")
		}
	}
	if m.Begin == m.End {
		return errors.New("the beginning and ending instruction markers must differ")
	}
	return nil
}

// markers reads both the current pair and pairs still installed in agents,
// including agents in stopped WSL distros. The current pair is written first.
func (l *Library) markers() []InstructionMarkers {
	out := []InstructionMarkers{l.Instructions.Markers.effective()}
	add := func(m InstructionMarkers) {
		if !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	add(defaultMarkers())
	for _, a := range l.Applied {
		if a != nil && a.InstrMarkers != nil {
			add(*a.InstrMarkers)
		}
	}
	return out
}

// setText leaves generated blocks out, including those still using an old
// pair in an agent linked to the source file (#2349).
func (l *Library) setText(path string) string { return own(readText(path), l.markers()...) }

func markerPairs(markers []InstructionMarkers) []InstructionMarkers {
	if len(markers) == 0 {
		return []InstructionMarkers{defaultMarkers()}
	}
	return markers
}

func (l *Library) hasInstructionMarker(text string) bool {
	for _, m := range l.markers() {
		if strings.Contains(text, m.Begin) || strings.Contains(text, m.End) {
			return true
		}
	}
	return false
}

func (l *Library) previousMarkers(id string) []InstructionMarkers {
	out := []InstructionMarkers{defaultMarkers()}
	if a := l.Applied[id]; a != nil && a.InstrMarkers != nil {
		out = append([]InstructionMarkers{*a.InstrMarkers}, out...)
	}
	return out
}

func (m InstructionMarkers) checkText(text string) error {
	if strings.Contains(text, m.Begin) || strings.Contains(text, m.End) {
		return errors.New("instruction text cannot contain its block markers")
	}
	return nil
}

func (l *Library) checkPersonalMarkers(id, file string, m InstructionMarkers) error {
	if err := m.checkText(own(file, l.previousMarkers(id)...)); err != nil {
		return errors.New("instruction markers already appear in personal text")
	}
	return nil
}

func (l *Library) checkMarkerFiles(m InstructionMarkers, agents []string) error {
	for _, t := range Targets() {
		if t.Instructions == "" || !slices.Contains(agents, t.Agent.ID) {
			continue
		}
		b, err := edit.Read(t.Instructions)
		if err != nil {
			return err
		}
		if err := l.checkPersonalMarkers(t.Agent.ID, string(b), m); err != nil {
			return fmt.Errorf("%s: %w", t.Instructions, err)
		}
	}
	return nil
}

// Refuse delimiters already used by the user's text rather than letting a
// later sync mistake that text for the library's block.
func (l *Library) checkMarkerChange(c InstructionsChange) error {
	m := l.Instructions.Markers.effective()
	if c.Markers != nil {
		m = c.Markers.effective()
		agents := c.Agents
		if agents == nil {
			agents = l.Instructions.Agents
		}
		if err := l.checkMarkerFiles(m, agents); err != nil {
			return err
		}
	}
	texts := map[string]string{}
	for _, s := range l.sets() {
		texts[s.ID] = l.setText(l.textPath(s.ID))
	}
	for id, x := range c.Texts {
		if x != nil {
			texts[id] = *x
		}
	}
	if c.Shared != nil {
		id := l.active()
		if c.Activate != "" {
			id = c.Activate
		}
		texts[id] = *c.Shared
	}
	delete(texts, c.Remove)
	for _, text := range texts {
		if err := m.checkText(text); err != nil {
			return err
		}
	}
	extra := extras()
	for id, text := range c.Extra {
		if text != nil {
			extra[id] = *text
		}
	}
	for _, text := range extra {
		if err := m.checkText(text); err != nil {
			return err
		}
	}
	return nil
}
