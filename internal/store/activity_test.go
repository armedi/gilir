package store

import (
	"context"
	"errors"
	"testing"
)

func TestActivityAfterPaginationAndHookCursorInitialization(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	latest, err := s.LatestActivityID(ctx)
	if err != nil {
		t.Fatalf("LatestActivityID empty: %v", err)
	}
	if latest != 0 {
		t.Fatalf("latest activity on empty store = %d, want 0", latest)
	}
	cursor, initialized, err := s.HookCursor(ctx, "deploy")
	if err != nil || initialized || cursor != 0 {
		t.Fatalf("missing HookCursor = (%d, %v, %v), want (0,false,nil)", cursor, initialized, err)
	}
	if err := s.SaveHookCursor(ctx, "  ", 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank hook error = %v, want ErrInvalid", err)
	}
	if err := s.SaveHookCursor(ctx, "deploy", -1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative cursor error = %v, want ErrInvalid", err)
	}

	first := mustCreateProject(t, s, "one")
	second := mustCreateProject(t, s, "two")
	third := mustCreateProject(t, s, "three")
	latest, err = s.LatestActivityID(ctx)
	if err != nil {
		t.Fatalf("LatestActivityID: %v", err)
	}
	if latest <= 0 {
		t.Fatalf("latest activity = %d", latest)
	}
	beforeCursorSave := latest

	if err := s.SaveHookCursor(ctx, "deploy", second.ID); err != nil {
		t.Fatalf("SaveHookCursor: %v", err)
	}
	cursor, initialized, err = s.HookCursor(ctx, "deploy")
	if err != nil || !initialized || cursor != second.ID {
		t.Fatalf("HookCursor = (%d, %v, %v), want (%d,true,nil)", cursor, initialized, err, second.ID)
	}
	afterCursorSave, err := s.LatestActivityID(ctx)
	if err != nil {
		t.Fatalf("LatestActivityID after cursor save: %v", err)
	}
	if afterCursorSave != beforeCursorSave {
		t.Fatalf("saving a hook cursor wrote activity: %d -> %d", beforeCursorSave, afterCursorSave)
	}
	if err := s.SaveHookCursor(ctx, "deploy", first.ID); err != nil {
		t.Fatalf("lower SaveHookCursor: %v", err)
	}
	cursor, initialized, err = s.HookCursor(ctx, "deploy")
	if err != nil || !initialized || cursor != second.ID {
		t.Fatalf("lower cursor regressed: (%d, %v, %v)", cursor, initialized, err)
	}
	if err := s.SaveHookCursor(ctx, "deploy", third.ID); err != nil {
		t.Fatalf("advance cursor: %v", err)
	}
	cursor, initialized, err = s.HookCursor(ctx, "deploy")
	if err != nil || !initialized || cursor != third.ID {
		t.Fatalf("advanced cursor = (%d, %v, %v)", cursor, initialized, err)
	}

	events, err := s.ActivityAfter(ctx, 0, 2)
	if err != nil {
		t.Fatalf("ActivityAfter page one: %v", err)
	}
	if len(events) != 2 || events[0].ID >= events[1].ID {
		t.Fatalf("first page = %+v", events)
	}
	if events[0].ProjectID == nil || *events[0].ProjectID != first.ID {
		t.Fatalf("first event project = %+v", events[0].ProjectID)
	}
	if events[0].Data == nil || events[0].Data["name"] != "one" {
		t.Fatalf("first event data = %+v", events[0].Data)
	}
	secondPage, err := s.ActivityAfter(ctx, events[len(events)-1].ID, 2)
	if err != nil {
		t.Fatalf("ActivityAfter page two: %v", err)
	}
	thirdPage, err := s.ActivityAfter(ctx, secondPage[len(secondPage)-1].ID, 2)
	if err != nil {
		t.Fatalf("ActivityAfter page three: %v", err)
	}
	if len(thirdPage) != 0 || thirdPage == nil {
		t.Fatalf("empty activity page = %#v, want non-nil empty", thirdPage)
	}
	if _, err := s.ActivityAfter(ctx, -1, 10); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative activity cursor error = %v, want ErrInvalid", err)
	}

	newHook, initialized, err := s.HookCursor(ctx, "notifications")
	if err != nil || initialized || newHook != 0 {
		t.Fatalf("new hook cursor = (%d,%v,%v)", newHook, initialized, err)
	}
	start, err := s.LatestActivityID(ctx)
	if err != nil {
		t.Fatalf("start cursor LatestActivityID: %v", err)
	}
	if err := s.SaveHookCursor(ctx, "notifications", start); err != nil {
		t.Fatalf("initialize new hook: %v", err)
	}
	mustCreateProject(t, s, "four")
	freshEvents, err := s.ActivityAfter(ctx, start, 10)
	if err != nil {
		t.Fatalf("ActivityAfter new hook: %v", err)
	}
	if len(freshEvents) != 1 || freshEvents[0].Data["name"] != "four" {
		t.Fatalf("new hook saw history or missed new event: %+v", freshEvents)
	}
	if freshEvents[0].Actor != "alice" {
		t.Fatalf("actor visibility lost: %+v", freshEvents[0])
	}
	if cursor, initialized, err = s.HookCursor(ctx, "notifications"); err != nil || !initialized || cursor != start {
		t.Fatalf("new hook cursor = (%d,%v,%v)", cursor, initialized, err)
	}
}
