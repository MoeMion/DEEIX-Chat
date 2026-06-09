package memory

import (
	"context"
	"testing"
	"time"
)

func TestGenerationStreamRegisterDoesNotMarkCanceled(t *testing.T) {
	cache := New()
	ctx := context.Background()
	runID := "run_memory_cancel_state"

	if err := cache.RegisterGenerationStream(ctx, runID, 7, time.Minute); err != nil {
		t.Fatalf("register generation stream: %v", err)
	}

	if canceled, err := cache.IsGenerationStreamCanceled(ctx, runID); err != nil || canceled {
		t.Fatalf("newly registered stream canceled=%v err=%v, want false nil", canceled, err)
	}
	if active, err := cache.IsGenerationStreamActive(ctx, runID); err != nil || active {
		t.Fatalf("newly registered stream active=%v err=%v, want false nil", active, err)
	}
	if ownerID, ok, err := cache.GetGenerationStreamOwner(ctx, runID); err != nil || !ok || ownerID != 7 {
		t.Fatalf("owner=(%d,%v) err=%v, want (7,true) nil", ownerID, ok, err)
	}

	if err := cache.RequestGenerationStreamCancel(ctx, runID, time.Minute); err != nil {
		t.Fatalf("request cancel: %v", err)
	}
	if canceled, err := cache.IsGenerationStreamCanceled(ctx, runID); err != nil || !canceled {
		t.Fatalf("requested stream canceled=%v err=%v, want true nil", canceled, err)
	}

	if err := cache.RegisterGenerationStream(ctx, runID, 7, time.Minute); err != nil {
		t.Fatalf("register generation stream after cancel: %v", err)
	}
	if canceled, err := cache.IsGenerationStreamCanceled(ctx, runID); err != nil || canceled {
		t.Fatalf("re-registered stream canceled=%v err=%v, want false nil", canceled, err)
	}
}

func TestGenerationStreamClearActiveMarksInactive(t *testing.T) {
	cache := New()
	ctx := context.Background()
	runID := "run_memory_active_state"

	if err := cache.RegisterGenerationStream(ctx, runID, 7, time.Minute); err != nil {
		t.Fatalf("register generation stream: %v", err)
	}
	if err := cache.TouchGenerationStreamActive(ctx, runID, time.Minute); err != nil {
		t.Fatalf("touch active stream: %v", err)
	}
	if active, err := cache.IsGenerationStreamActive(ctx, runID); err != nil || !active {
		t.Fatalf("touched stream active=%v err=%v, want true nil", active, err)
	}

	if err := cache.ClearGenerationStreamActive(ctx, runID); err != nil {
		t.Fatalf("clear active stream: %v", err)
	}
	if active, err := cache.IsGenerationStreamActive(ctx, runID); err != nil || active {
		t.Fatalf("cleared stream active=%v err=%v, want false nil", active, err)
	}
}
