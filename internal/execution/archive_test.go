package execution

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveManagerRoundTripsFilesAndRejectsTraversal(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "dist", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "dist", "app.txt"), []byte("app"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "dist", "nested", "skip.log"), []byte("skip"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager, err := newArchiveManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	object, err := manager.CreateZip(workspace, []string{"dist/**"}, []string{"**/*.log"})
	if err != nil {
		t.Fatal(err)
	}
	if object.FileCount != 1 || len(object.SHA256) != 64 {
		t.Fatalf("archive object = %#v", object)
	}
	destination := t.TempDir()
	if err := manager.ExtractZip(object.Key, destination); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(destination, "dist", "app.txt"))
	if err != nil || string(content) != "app" {
		t.Fatalf("round trip content=%q err=%v", content, err)
	}
	key, target, err := manager.nextObjectPath("artifacts", ".zip")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(target)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("../escape.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = entry.Write([]byte("escape"))
	_ = writer.Close()
	_ = file.Close()
	if err := manager.ExtractZip(key, t.TempDir()); err == nil {
		t.Fatal("traversing ZIP entry was accepted")
	}
}
