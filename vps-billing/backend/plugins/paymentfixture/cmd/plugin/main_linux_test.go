package main

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestFixtureKeyReadsOnlyPrivateCredential(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "payment-fixture-key")
	want := bytes.Repeat([]byte{0x81}, 32)
	if err := os.WriteFile(path, want, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := fixtureKey(directory)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("private credential rejected: length=%d err=%v", len(got), err)
	}
	if err = os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	if got, err = fixtureKey(directory); err != nil || !bytes.Equal(got, want) {
		t.Fatal("read-only private credential rejected", err)
	}
	for _, directory := range []string{"", ".", "/tmp/../tmp", "/tmp\n"} {
		if got, err := fixtureKey(directory); err == nil || len(got) != 0 {
			t.Fatalf("invalid credential directory accepted: %q", directory)
		}
	}
}

func TestFixtureKeyRejectsUnsafeFiles(t *testing.T) {
	tests := map[string]func(string) error{
		"short":    func(path string) error { return os.WriteFile(path, bytes.Repeat([]byte{'a'}, 31), 0600) },
		"oversize": func(path string) error { return os.WriteFile(path, bytes.Repeat([]byte{'a'}, 4097), 0600) },
		"public": func(path string) error {
			if err := os.WriteFile(path, bytes.Repeat([]byte{'a'}, 32), 0600); err != nil {
				return err
			}
			return os.Chmod(path, 0644)
		},
		"group readable without worker ACL": func(path string) error {
			if err := os.WriteFile(path, bytes.Repeat([]byte{'a'}, 32), 0600); err != nil {
				return err
			}
			return os.Chmod(path, 0440)
		},
		"symlink": func(path string) error {
			if err := os.WriteFile(path+"-target", bytes.Repeat([]byte{'a'}, 32), 0600); err != nil {
				return err
			}
			return os.Symlink(path+"-target", path)
		},
		"hardlink": func(path string) error {
			if err := os.WriteFile(path+"-target", bytes.Repeat([]byte{'a'}, 32), 0600); err != nil {
				return err
			}
			return os.Link(path+"-target", path)
		},
		"directory": func(path string) error { return os.Mkdir(path, 0700) },
		"fifo":      func(path string) error { return syscall.Mkfifo(path, 0600) },
	}
	for name, create := range tests {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			if err := create(filepath.Join(directory, "payment-fixture-key")); err != nil {
				t.Fatal(err)
			}
			if got, err := fixtureKey(directory); err == nil || len(got) != 0 {
				t.Fatal("unsafe credential accepted")
			}
		})
	}
}
