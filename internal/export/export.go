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
	} else if summary := outlineSummary(st); summary != "" {
		out.WriteString(summary)
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
		if strings.TrimSpace(t.Title) == "" && strings.TrimSpace(t.Summary) == "" && len(t.Points) == 0 && len(t.Assertions) == 0 {
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
		if points := cleanStrings(t.Points); len(points) > 0 {
			out.WriteString("Discussion points:\n\n")
			for _, point := range points {
				fmt.Fprintf(&out, "- %s\n", point)
			}
			out.WriteString("\n")
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

func outlineSummary(st analysis.State) string {
	topics := append([]analysis.Topic(nil), st.Past...)
	if st.Current.Title != "" || st.Current.Summary != "" || len(st.Current.Points) > 0 {
		topics = append(topics, st.Current)
	}
	var out strings.Builder
	for _, topic := range topics {
		title := strings.TrimSpace(topic.Title)
		detail := strings.TrimSpace(topic.Summary)
		if detail == "" {
			points := cleanStrings(topic.Points)
			detail = strings.Join(points, " ")
		}
		if title == "" && detail == "" {
			continue
		}
		if title == "" {
			fmt.Fprintf(&out, "- %s\n", detail)
		} else if detail == "" {
			fmt.Fprintf(&out, "- **%s**\n", title)
		} else {
			fmt.Fprintf(&out, "- **%s:** %s\n", title, detail)
		}
	}
	return strings.TrimSpace(out.String())
}

func cleanStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

// TranscriptMarkdown renders the pre-meeting context captured with the session
// followed by every persisted transcript segment. It intentionally excludes
// generated analysis and live notes so the result is a clean source document.
func TranscriptMarkdown(b store.SessionBundle) string {
	var out strings.Builder
	title := strings.TrimSpace(b.Session.Title)
	if title == "" {
		title = "Meeting"
	}
	fmt.Fprintf(&out, "# %s - %s\n\n", title, sessionDate(b.Session.StartedAt))
	out.WriteString("## Meeting context\n\n")
	snapshot := b.ContextSnapshot
	switch {
	case !snapshot.Captured:
		out.WriteString("_Original meeting context is unavailable for this legacy session._\n\n")
	case strings.TrimSpace(snapshot.Summary) == "" && strings.TrimSpace(snapshot.People) == "" && strings.TrimSpace(snapshot.Notes) == "":
		out.WriteString("_No pre-meeting context was provided._\n\n")
	default:
		writeContextField(&out, "Summary / agenda", snapshot.Summary)
		writeContextField(&out, "People", snapshot.People)
		writeContextField(&out, "Notes", snapshot.Notes)
	}

	out.WriteString("## Transcript\n\n")
	if len(b.Segments) == 0 {
		out.WriteString("_No transcript was captured._\n")
		return out.String()
	}
	for _, segment := range b.Segments {
		text := strings.TrimSpace(segment.Text)
		if text == "" {
			continue
		}
		speaker := strings.TrimSpace(segment.Source)
		if speaker == "" {
			speaker = "Unknown"
		}
		fmt.Fprintf(&out, "[%s] **%s:** %s\n\n", transcriptTime(segment.StartMs), speaker, text)
	}
	return strings.TrimRight(out.String(), "\n") + "\n"
}

func writeContextField(out *strings.Builder, title, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	fmt.Fprintf(out, "### %s\n\n%s\n\n", title, value)
}

func transcriptTime(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	totalSeconds := ms / 1000
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
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
	if st.Current.Title != "" || st.Current.Summary != "" || len(st.Current.Points) > 0 || len(st.Current.Assertions) > 0 {
		topics = append(topics, st.Current)
	}
	topics = append(topics, st.Past...)
	return topics
}
