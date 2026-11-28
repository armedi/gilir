package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/armedi/gilir/internal/store"
)

func writeTaskDetail(w io.Writer, value any) error {
	// Read the service response into the shared task model. --json bypasses
	// this renderer and writes the original response.
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var detail store.TaskDetail
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber() // Keep comment IDs in activity data exact.
	if err := decoder.Decode(&detail); err != nil || detail.Task.ID == "" {
		return writeHumanErr(w, value)
	}

	var out strings.Builder
	task := detail.Task
	header := []string{task.ID, issueLabel(string(task.Status)), issueLabel(string(task.Priority)) + " priority"}
	for _, marking := range task.Markings {
		header = append(header, issueLabel(marking))
	}
	writeText(&out, strings.Join(header, " · "))
	writeText(&out, task.Title)
	if task.Body != "" {
		fmt.Fprintln(&out)
		writeText(&out, task.Body)
	}

	if len(detail.Blockers) > 0 {
		fmt.Fprintf(&out, "\nBlockers\n  %s\n", strings.Join(detail.Blockers, ", "))
	}
	if task.Worktree != nil && !slices.ContainsFunc(detail.Sessions, func(session store.Session) bool {
		return session.Worktree != nil && *session.Worktree == *task.Worktree
	}) {
		fmt.Fprintf(&out, "\nWorktree\n  %s\n", *task.Worktree)
	}
	if detail.Attempt != nil {
		fmt.Fprintf(&out, "\nCurrent attempt\n  %s%s\n", issueTime(detail.Attempt.CreatedAt), detail.Attempt.ID)
	}
	if len(detail.Sessions) > 0 {
		fmt.Fprintln(&out, "\nSessions")
		for _, session := range detail.Sessions {
			fmt.Fprintf(&out, "  %s%s · %s\n", issueTime(session.CreatedAt), sessionState(session.State), session.ID)
			if session.Worktree != nil {
				fmt.Fprintf(&out, "    Worktree: %s\n", *session.Worktree)
			}
			if session.URL != "" {
				fmt.Fprintf(&out, "    %s\n", session.URL)
			}
		}
	}
	writeIssueActivity(&out, detail.Activity)
	_, err = io.WriteString(w, out.String())
	return err
}

func issueLabel(value string) string {
	label := []rune(strings.NewReplacer("-", " ", "_", " ", ".", " ").Replace(value))
	if len(label) > 0 {
		label[0] = unicode.ToUpper(label[0])
	}
	return string(label)
}

func issueTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format("02 Jan 2006 15:04 UTC") + " · "
}

func sessionState(state string) string {
	switch store.AttemptOutcome(state) {
	case store.OutcomeSuccess:
		return "Succeeded"
	case store.OutcomeFailure:
		return "Failed"
	default:
		return issueLabel(state)
	}
}

func writeIssueActivity(out *strings.Builder, events []store.Activity) {
	// Finishing a successful attempt writes both events with the same actor
	// and timestamp, sometimes with a marking change between them.
	type eventMoment struct {
		timestamp time.Time
		actor     string
	}
	reviewMoves := make(map[eventMoment]bool)
	for _, event := range events {
		if event.Type == store.ActivityTaskStatusChanged && event.Data["reason"] == "dispatch_success" {
			reviewMoves[eventMoment{event.Timestamp, event.Actor}] = true
		}
	}

	heading := "\nActivity"
	for _, event := range events {
		if event.Type == store.ActivitySessionOutcome && event.Data["outcome"] == string(store.OutcomeSuccess) &&
			reviewMoves[eventMoment{event.Timestamp, event.Actor}] {
			continue
		}
		label, body := issueActivity(event)
		if label == "" {
			continue
		}
		if heading != "" {
			fmt.Fprintln(out, heading)
			heading = ""
		}
		fmt.Fprintf(out, "  %s%s · %s\n", issueTime(event.Timestamp), label, event.Actor)
		if body != "" {
			fmt.Fprintln(out)
			writeText(out, body)
			fmt.Fprintln(out)
		}
	}
}

func issueActivity(event store.Activity) (label, body string) {
	text := func(key string) string {
		value, _ := event.Data[key].(string)
		return value
	}
	switch event.Type {
	case store.ActivityTaskClaimed, store.ActivityTaskWorktree, store.ActivitySessionLinked:
		// The dispatch attempt and sessions already have their own sections.
		return "", ""
	case store.ActivityTaskCreated:
		return "Created task", ""
	case store.ActivityTaskUpdated:
		return "Updated task", ""
	case store.ActivityTaskStatusChanged:
		status := issueLabel(text("new"))
		if text("reason") == "dispatch_success" {
			return "Session succeeded; moved to " + status, ""
		}
		return "Moved to " + status, ""
	case store.ActivityTaskMarking:
		marking := issueLabel(text("marking"))
		if event.Data["new"] == true {
			return "Marked: " + marking, ""
		}
		return "Cleared marking: " + marking, ""
	case store.ActivityTaskCommented:
		return fmt.Sprintf("Comment #%d", event.ID), text("message")
	case store.ActivityTaskCommentEdited:
		return fmt.Sprintf("Edited comment #%v", event.Data["comment_id"]), text("message")
	case store.ActivityTaskCommentRemoved:
		return fmt.Sprintf("Removed comment #%v", event.Data["comment_id"]), ""
	case store.ActivityDependencyAdded:
		return "Added blocker " + text("depends_on_id"), ""
	case store.ActivityDependencyRemoved:
		return "Removed blocker " + text("depends_on_id"), ""
	case store.ActivitySessionOutcome:
		return "Session: " + sessionState(text("outcome")), text("detail")
	default:
		return issueLabel(event.Type), ""
	}
}
