// payment-manifest writes the manifest for a locally built payment template.
// It hashes the file without executing it. Core does not import the template.
package main

import (
	"aster/billing/pkg/pluginapi"
	paymenttemplate "aster/billing/plugins/payment-template"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
)

func main() {
	plugin := flag.String("plugin", "", "template")
	binary := flag.String("binary", "", "built Linux executable to hash, never execute")
	flag.Parse()
	if *plugin != "template" || *binary == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "Specify --plugin and --binary.")
		os.Exit(2)
	}
	f, err := os.Open(*binary)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Cannot read executable.")
		os.Exit(1)
	}
	defer f.Close()
	digest := sha256.New()
	n, err := io.Copy(digest, io.LimitReader(f, (64<<20)+1))
	if err != nil || n == 0 || n > 64<<20 {
		fmt.Fprintln(os.Stderr, "Invalid executable size.")
		os.Exit(1)
	}
	manifest := paymenttemplate.Manifest(hex.EncodeToString(digest.Sum(nil)))
	if manifest.Validate(pluginapi.CoreVersion) != nil {
		fmt.Fprintln(os.Stderr, "Invalid template manifest.")
		os.Exit(1)
	}
	if json.NewEncoder(os.Stdout).Encode(manifest) != nil {
		os.Exit(1)
	}
}
