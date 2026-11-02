package astercloud

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinnedCloudServiceSDK(t *testing.T) {
	root := "vendor-source"
	raw, err := os.ReadFile(filepath.Join(root, "CLOUD_SOURCE.json"))
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Repository, Commit, Path string
		Files                    map[string]string
	}
	if err = json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	if source.Repository != "xiaoqiqixi/betteridcsys-cloud" || len(source.Commit) != 40 || source.Path != "contracts/billing" || len(source.Files) < 17 {
		t.Fatal("incomplete cloud SDK provenance")
	}
	for path, expected := range source.Files {
		paths := []string{filepath.Join(root, "cloud-billing", path)}
		if strings.HasPrefix(path, "serviceapi/") || path == "internal/controllers/service.go" {
			paths = append(paths, path)
		}
		for _, file := range paths {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if file == path {
				data = []byte(strings.ReplaceAll(string(data), "aster/billing/plugins/server.aster-cloud/", "aster/billing/"))
			}
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != expected {
				t.Errorf("pinned SDK file changed: %s", file)
			}
		}
	}
}
