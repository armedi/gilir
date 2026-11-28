package watcher

import (
	"fmt"
	"slices"
	"strings"

	"github.com/armedi/gilir/internal/store"
)

// Preserve Markdown in XML text while preventing task content from closing a tag.
var promptXML = strings.NewReplacer("&", "&amp;", "<", "&lt;", "]]>", "]]&gt;")

func dispatchPrompt(detail store.TaskDetail, attemptID string) string {
	task := detail.Task
	var prompt strings.Builder
	fmt.Fprintf(&prompt, "Work on %s. Run `gilir skill` before starting.\n\n"+
		"<task id=%q>\n<title>%s</title>\n<body>\n%s\n</body>\n</task>",
		task.ID, task.ID, promptXML.Replace(task.Title), promptXML.Replace(strings.TrimSuffix(task.Body, "\n")))

	note := dispatchNote(detail.Activity, attemptID)
	if note != nil {
		fmt.Fprintf(&prompt, "\n\n<%s>\n%s\n</%s>", note.tag, promptXML.Replace(note.message), note.tag)
	}

	if slices.ContainsFunc(detail.Sessions, func(session store.Session) bool {
		return session.AttemptID != attemptID && session.State != string(store.OutcomeAbandoned)
	}) {
		fmt.Fprintf(&prompt, "\n\nContinue from the existing work in this worktree. Before editing, read `sessions` and `activity` in `gilir show %s` for the previous attempt’s handoff.", task.ID)
	} else if slices.ContainsFunc(detail.Activity, func(event store.Activity) bool {
		return event.Type == store.ActivityTaskCommented && (note == nil || event.ID != note.commentID) &&
			event.Actor != store.ActorSystem && !strings.HasPrefix(event.Actor, "agent:") && !strings.HasPrefix(event.Actor, "hook:")
	}) {
		fmt.Fprintf(&prompt, "\n\nRead the task comments in `gilir show %s` before editing.", task.ID)
	}
	return prompt.String()
}

type attemptNote struct {
	tag       string
	message   string
	commentID int64
}

// Look for the latest review or unblock in the run-up to this claim. The
// store writes its comment immediately before the action in the same
// transaction. Show projects edits and removals, so also require consecutive
// event IDs rather than mistaking an unrelated comment for a removed one.
func dispatchNote(activity []store.Activity, attemptID string) *attemptNote {
	claim := slices.IndexFunc(activity, func(event store.Activity) bool {
		return event.Type == store.ActivityTaskClaimed && event.Data["attempt_id"] == attemptID
	})
	for i := claim - 1; i > 0; i-- {
		event := activity[i]
		comment := activity[i-1]
		var tag string
		switch {
		case event.Type == store.ActivityTaskClaimed:
			return nil
		case event.Type == store.ActivityTaskStatusChanged &&
			event.Data["old"] == string(store.StatusInReview) && event.Data["new"] == string(store.StatusTodo):
			tag = "changes_requested"
		case event.Type == store.ActivityTaskMarking &&
			event.Data["marking"] == store.MarkingBlocked && event.Data["new"] == false:
			if comment.Data["purpose"] != store.CommentPurposeUnblock {
				return nil
			}
			tag = "unblock_note"
		default:
			continue
		}
		if comment.Type == store.ActivityTaskCommented && comment.ID+1 == event.ID &&
			comment.Actor == event.Actor && comment.Timestamp.Equal(event.Timestamp) {
			if message, ok := comment.Data["message"].(string); ok && message != "" {
				return &attemptNote{tag: tag, message: message, commentID: comment.ID}
			}
		}
		return nil
	}
	return nil
}
