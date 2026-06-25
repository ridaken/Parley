// Package export renders persisted sessions into portable document formats.
package export

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tomvokac/parley/internal/analysis"
	"github.com/tomvokac/parley/internal/store"
)

// Markdown renders the saved meeting notes as Markdown. It intentionally omits
// the raw transcript and audio details; exports are minutes, not a data dump.
func Markdown(b store.SessionBundle) string {
	var st analysis.State
	if strings.TrimSpace(b.AnalysisJSON) != "" {
		_ = json.Unmarshal([]byte(b.AnalysisJSON), &st)
	}

	var out strings.Builder
	title := strings.TrimSpace(b.Session.Title)
	if title == "" {
		title = "Meeting"
	}
	fmt.Fprintf(&out, "# %s - %s\n\n", title, sessionDate(b.Session.StartedAt))

	out.WriteString("## Summary\n\n")
	if strings.TrimSpace(st.Summary) != "" {
		out.WriteString(strings.TrimSpace(st.Summary))
		out.WriteString("\n\n")
	} else {
		out.WriteString("_No summary yet._\n\n")
	}

	out.WriteString("## Action items\n\n")
	if len(st.ActionItems) == 0 {
		out.WriteString("_No action items captured._\n\n")
	} else {
		for _, a := range st.ActionItems {
			text := strings.TrimSpace(a.Text)
			if text == "" {
				continue
			}
			owner := strings.TrimSpace(a.Owner)
			if owner == "" {
				owner = "unassigned"
			}
			fmt.Fprintf(&out, "- [ ] %s - %s\n", text, owner)
		}
		out.WriteString("\n")
	}

	out.WriteString("## Topics covered\n\n")
	topics := topicsCovered(st)
	if len(topics) == 0 {
		out.WriteString("_No topics captured._\n")
		return out.String()
	}
	for _, t := range topics {
		if strings.TrimSpace(t.Title) == "" && strings.TrimSpace(t.Summary) == "" && len(t.Assertions) == 0 {
			continue
		}
		title := strings.TrimSpace(t.Title)
		if title == "" {
			title = "Untitled topic"
		}
		fmt.Fprintf(&out, "### %s\n\n", title)
		if strings.TrimSpace(t.Summary) != "" {
			out.WriteString(strings.TrimSpace(t.Summary))
			out.WriteString("\n\n")
		}
		if len(t.Assertions) > 0 {
			out.WriteString("Key assertions:\n\n")
			for _, a := range t.Assertions {
				text := strings.TrimSpace(a.Text)
				if text == "" {
					continue
				}
				speaker := strings.TrimSpace(a.Speaker)
				if speaker == "" {
					fmt.Fprintf(&out, "- %s\n", text)
				} else {
					fmt.Fprintf(&out, "- **%s:** %s\n", speaker, text)
				}
			}
			out.WriteString("\n")
		}
	}
	return strings.TrimRight(out.String(), "\n") + "\n"
}

func sessionDate(startedAt string) string {
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(startedAt)); err == nil {
		return t.Local().Format("Jan 2, 2006")
	}
	if strings.TrimSpace(startedAt) != "" {
		return startedAt
	}
	return time.Now().Format("Jan 2, 2006")
}

func topicsCovered(st analysis.State) []analysis.Topic {
	topics := make([]analysis.Topic, 0, 1+len(st.Past))
	if st.Current.Title != "" || st.Current.Summary != "" || len(st.Current.Assertions) > 0 {
		topics = append(topics, st.Current)
	}
	topics = append(topics, st.Past...)
	return topics
}
