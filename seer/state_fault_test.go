package seer

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteStateAtomicFaultBoundariesPreserveKnownGoodFiles(t *testing.T) {
	const (
		oldBackup = iota
		oldPrimary
		newPrimary
	)
	tests := []struct {
		stage       string
		wantPrimary int
		wantBackup  int
	}{
		{stage: "backup-create", wantPrimary: oldPrimary, wantBackup: oldBackup},
		{stage: "backup-chmod", wantPrimary: oldPrimary, wantBackup: oldBackup},
		{stage: "backup-write", wantPrimary: oldPrimary, wantBackup: oldBackup},
		{stage: "backup-sync", wantPrimary: oldPrimary, wantBackup: oldBackup},
		{stage: "backup-close", wantPrimary: oldPrimary, wantBackup: oldBackup},
		{stage: "backup-rename", wantPrimary: oldPrimary, wantBackup: oldBackup},
		{stage: "backup-directory-sync", wantPrimary: oldPrimary, wantBackup: oldPrimary},
		{stage: "backup-cleanup", wantPrimary: oldPrimary, wantBackup: oldPrimary},
		{stage: "primary-create", wantPrimary: oldPrimary, wantBackup: oldPrimary},
		{stage: "primary-chmod", wantPrimary: oldPrimary, wantBackup: oldPrimary},
		{stage: "primary-encode", wantPrimary: oldPrimary, wantBackup: oldPrimary},
		{stage: "primary-flush", wantPrimary: oldPrimary, wantBackup: oldPrimary},
		{stage: "primary-sync", wantPrimary: oldPrimary, wantBackup: oldPrimary},
		{stage: "primary-close", wantPrimary: oldPrimary, wantBackup: oldPrimary},
		{stage: "primary-rename", wantPrimary: oldPrimary, wantBackup: oldPrimary},
		{stage: "primary-directory-sync", wantPrimary: newPrimary, wantBackup: oldPrimary},
		{stage: "primary-cleanup", wantPrimary: newPrimary, wantBackup: oldPrimary},
	}

	for _, tc := range tests {
		t.Run(tc.stage, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "state.json")
			if err := writeStateAtomic(path, fixtureState(10)); err != nil {
				t.Fatal(err)
			}
			backupBytes, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeStateAtomic(path, fixtureState(20)); err != nil {
				t.Fatal(err)
			}
			primaryBytes, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			expectedPath := filepath.Join(directory, "expected.json")
			if err := writeStateAtomic(expectedPath, fixtureState(30)); err != nil {
				t.Fatal(err)
			}
			newBytes, err := os.ReadFile(expectedPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(expectedPath); err != nil {
				t.Fatal(err)
			}

			injected := errors.New("injected " + tc.stage + " failure")
			operations := faultInjectedStateOps(tc.stage, injected)
			if err := writeStateAtomicWithOps(path, fixtureState(30), operations); !errors.Is(err, injected) {
				t.Fatalf("write error = %v, want %v", err, injected)
			}

			known := [][]byte{backupBytes, primaryBytes, newBytes}
			assertExactStateFile(t, path, known[tc.wantPrimary])
			assertExactStateFile(t, path+stateBackupSuffix, known[tc.wantBackup])
			entries, err := os.ReadDir(directory)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.Contains(entry.Name(), ".tmp-") {
					t.Fatalf("fault left temporary state artifact %q", entry.Name())
				}
			}
		})
	}
}

func assertExactStateFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s content changed\n got: %s\nwant: %s", filepath.Base(path), got, want)
	}
	if _, _, err := decodeState(got); err != nil {
		t.Fatalf("%s is not known-good state: %v", filepath.Base(path), err)
	}
}

type faultStateTemp struct {
	stateTempFile
	role     string
	stage    string
	injected error
}

func (f *faultStateTemp) Chmod(mode os.FileMode) error {
	if f.stage == f.role+"-chmod" {
		return f.injected
	}
	return f.stateTempFile.Chmod(mode)
}

func (f *faultStateTemp) Write(data []byte) (int, error) {
	writeStage := f.role + "-write"
	if f.role == "primary" {
		writeStage = "primary-flush"
	}
	if f.stage == writeStage {
		return 0, f.injected
	}
	return f.stateTempFile.Write(data)
}

func (f *faultStateTemp) Sync() error {
	if f.stage == f.role+"-sync" {
		return f.injected
	}
	return f.stateTempFile.Sync()
}

func (f *faultStateTemp) Close() error {
	if f.stage == f.role+"-close" {
		return errors.Join(f.stateTempFile.Close(), f.injected)
	}
	return f.stateTempFile.Close()
}

func faultInjectedStateOps(stage string, injected error) stateFileOps {
	operations := defaultStateFileOps()
	realCreate := operations.createTemp
	operations.createTemp = func(directory, pattern string) (stateTempFile, error) {
		role := "primary"
		if strings.Contains(pattern, stateBackupSuffix+".tmp-") {
			role = "backup"
		}
		if stage == role+"-create" {
			return nil, injected
		}
		file, err := realCreate(directory, pattern)
		if err != nil {
			return nil, err
		}
		return &faultStateTemp{stateTempFile: file, role: role, stage: stage, injected: injected}, nil
	}
	realEncode := operations.encodeJSON
	operations.encodeJSON = func(writer io.Writer, value any) error {
		if stage == "primary-encode" {
			return injected
		}
		return realEncode(writer, value)
	}
	realRename := operations.rename
	operations.rename = func(oldPath, newPath string) error {
		role := "primary"
		if strings.HasSuffix(newPath, stateBackupSuffix) {
			role = "backup"
		}
		if stage == role+"-rename" {
			return injected
		}
		return realRename(oldPath, newPath)
	}
	realSyncDirectory := operations.syncDirectory
	directorySyncs := 0
	operations.syncDirectory = func(directory string) error {
		directorySyncs++
		if err := realSyncDirectory(directory); err != nil {
			return err
		}
		if stage == "backup-directory-sync" && directorySyncs == 1 {
			return injected
		}
		if stage == "primary-directory-sync" && directorySyncs == 2 {
			return injected
		}
		return nil
	}
	realRemove := operations.remove
	operations.remove = func(path string) error {
		err := realRemove(path)
		role := "primary"
		if strings.Contains(filepath.Base(path), stateBackupSuffix+".tmp-") {
			role = "backup"
		}
		if stage == role+"-cleanup" {
			return injected
		}
		return err
	}
	return operations
}
