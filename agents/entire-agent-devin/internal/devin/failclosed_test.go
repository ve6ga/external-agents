package devin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareTranscript_KeepsNativeTranscriptUntouched(t *testing.T) {
	dataDir, db := setupSessionsDB(t)
	setDevinDataDir(t, dataDir)

	sessionID := "native-guard"
	insertSession(t, db, sessionID, "SWE-1.7", 1)
	insertNode(t, db, sessionID, 1, 0, `{"role":"user","content":"native guard prompt"}`, "")

	nativeDir := filepath.Join(dataDir, "devin", "cli", "transcripts")
	if err := os.MkdirAll(nativeDir, 0o750); err != nil {
		t.Fatal(err)
	}
	nativePath := filepath.Join(nativeDir, sessionID+".json")
	nativeBytes := []byte(`{"schema_version":"ATIF-v1.7","session_id":"native-guard","steps":[]}`)
	if err := os.WriteFile(nativePath, nativeBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	ref := sessionRefForID(sessionID)
	if ref == "" {
		t.Fatal("sessionRefForID returned empty")
	}
	if !strings.Contains(ref, "entire-transcripts") {
		t.Fatalf("sessionRef = %q, want Entire-owned snapshot path", ref)
	}
	if ref == nativePath {
		t.Fatalf("sessionRef collides with native transcript path %q", nativePath)
	}

	d := New()
	if err := d.PrepareTranscript(ref); err != nil {
		t.Fatalf("PrepareTranscript: %v", err)
	}

	got, err := os.ReadFile(nativePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(nativeBytes) {
		t.Error("native Devin transcript was modified")
	}
	tx, err := parseTranscriptFile(ref)
	if err != nil {
		t.Fatalf("snapshot not written: %v", err)
	}
	if len(tx.Steps) != 1 {
		t.Fatalf("snapshot steps = %d, want 1", len(tx.Steps))
	}
}

func TestPrepareTranscript_RegeneratesSnapshotFromStore(t *testing.T) {
	dataDir, db := setupSessionsDB(t)
	setDevinDataDir(t, dataDir)

	sessionID := "regen-snapshot"
	insertSession(t, db, sessionID, "SWE-1.7", 1)
	insertNode(t, db, sessionID, 1, 0, `{"role":"user","content":"first"}`, "")

	d := New()
	ref := sessionRefForID(sessionID)
	if err := d.PrepareTranscript(ref); err != nil {
		t.Fatalf("PrepareTranscript #1: %v", err)
	}
	tx, err := parseTranscriptFile(ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(tx.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(tx.Steps))
	}

	insertNode(t, db, sessionID, 2, 1, `{"role":"assistant","content":"second"}`, "")
	if _, err := db.Exec(`UPDATE sessions SET main_chain_id = 2 WHERE id = ?`, sessionID); err != nil {
		t.Fatal(err)
	}
	if err := d.PrepareTranscript(ref); err != nil {
		t.Fatalf("PrepareTranscript #2: %v", err)
	}
	tx, err = parseTranscriptFile(ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(tx.Steps) != 2 {
		t.Fatalf("steps after regen = %d, want 2", len(tx.Steps))
	}
}

func TestPrepareTranscript_MissingStoreReturnsError(t *testing.T) {
	dataDir := t.TempDir()
	setDevinDataDir(t, dataDir)

	d := New()
	ref := sessionRefForID("ghost-session")
	if ref == "" {
		t.Fatal("sessionRefForID returned empty")
	}
	if err := os.MkdirAll(filepath.Dir(ref), 0o750); err != nil {
		t.Fatal(err)
	}
	prior := []byte(`{"schema_version":"ATIF-v1.7","session_id":"ghost-session","steps":[{"step_id":1,"source":"user","message":"prior"}]}`)
	if err := os.WriteFile(ref, prior, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := d.PrepareTranscript(ref); err == nil {
		t.Fatal("PrepareTranscript succeeded with no sessions.db, want error")
	}
	got, err := os.ReadFile(ref)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "prior") {
		t.Error("capture-failed marker retained prior snapshot content")
	}
	tx, err := parseTranscript(got)
	if err != nil {
		t.Fatalf("marker is not valid ATIF: %v", err)
	}
	if len(tx.Steps) != 1 {
		t.Fatalf("marker steps = %d, want 1", len(tx.Steps))
	}
	var step struct {
		Source  string `json:"source"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(tx.Steps[0], &step); err != nil {
		t.Fatal(err)
	}
	if step.Source != "system" || step.Message != "ENTIRE CAPTURE FAILED - transcript unavailable" {
		t.Errorf("marker step = %+v", step)
	}
}

func TestPrepareTranscript_CorruptStoreReturnsError(t *testing.T) {
	dataDir := t.TempDir()
	setDevinDataDir(t, dataDir)
	dbDir := filepath.Join(dataDir, "devin", "cli")
	if err := os.MkdirAll(dbDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "sessions.db"), []byte("this is not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}

	d := New()
	ref := sessionRefForID("corrupt-session")
	if err := os.MkdirAll(filepath.Dir(ref), 0o750); err != nil {
		t.Fatal(err)
	}
	prior := []byte(`{"schema_version":"ATIF-v1.7","session_id":"corrupt-session","steps":[]}`)
	if err := os.WriteFile(ref, prior, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := d.PrepareTranscript(ref); err == nil {
		t.Fatal("PrepareTranscript succeeded with corrupt sessions.db, want error")
	}
	got, err := os.ReadFile(ref)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), `"steps":[]`) {
		t.Error("corrupt-store marker kept prior empty-steps snapshot content")
	}
	if !strings.Contains(string(got), "ENTIRE CAPTURE FAILED - transcript unavailable") {
		t.Error("corrupt-store failure did not write capture-failed marker")
	}
}

func TestPrepareTranscript_RecoversAfterStoreAppears(t *testing.T) {
	dataDir := t.TempDir()
	setDevinDataDir(t, dataDir)

	d := New()
	ref := sessionRefForID("recovery-session")
	if err := d.PrepareTranscript(ref); err == nil {
		t.Fatal("PrepareTranscript succeeded with no sessions.db, want error")
	}

	db := setupSessionsDBAt(t, dataDir, "cli")
	sessionID := "recovery-session"
	insertSession(t, db, sessionID, "SWE-1.7", 1)
	insertNode(t, db, sessionID, 1, 0, `{"role":"user","content":"recovered"}`, "")

	if err := d.PrepareTranscript(ref); err != nil {
		t.Fatalf("PrepareTranscript after store appears: %v", err)
	}
	tx, err := parseTranscriptFile(ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(tx.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(tx.Steps))
	}
	data, err := os.ReadFile(ref)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "ENTIRE CAPTURE FAILED") {
		t.Error("capture-failed marker survived successful preparation")
	}
}
