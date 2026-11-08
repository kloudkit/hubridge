package main

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/kloudkit/hubridge/internal/protocol"
	"github.com/kloudkit/hubridge/internal/update"
)

const usage = `usage: go run ./tools/release <command>

  keygen --out <file>   write a new base64 ed25519 private key to <file> (mode 0600) and print its public key
  manifest --checksums <checksums.txt> --version <tag> --out <manifest.json>
                        write the release manifest and <manifest.json>.sig, signed with HUBRIDGE_SIGNING_KEY
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen(os.Args[2:])
	case "manifest":
		err = manifest(os.Args[2:])
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "release:", err)
		os.Exit(1)
	}
}

func keygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	out := fs.String("out", "", "private key file")
	fs.Parse(args)
	if *out == "" {
		return errors.New("pass --out")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(priv)); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Println(base64.StdEncoding.EncodeToString(pub))
	return nil
}

func manifest(args []string) error {
	fs := flag.NewFlagSet("manifest", flag.ExitOnError)
	sums := fs.String("checksums", "", "goreleaser checksums.txt")
	version := fs.String("version", "", "release tag")
	out := fs.String("out", "", "manifest.json to write")
	fs.Parse(args)
	if *sums == "" || *out == "" || !semver.IsValid(*version) {
		return errors.New("pass --checksums, --out and a --version tag such as v0.1.0")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("HUBRIDGE_SIGNING_KEY")))
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return errors.New("HUBRIDGE_SIGNING_KEY is not a base64 ed25519 private key")
	}
	key := ed25519.PrivateKey(raw)
	if pub, _ := base64.StdEncoding.DecodeString(update.PublicKey); !key.Public().(ed25519.PublicKey).Equal(ed25519.PublicKey(pub)) {
		return errors.New("HUBRIDGE_SIGNING_KEY does not match update.PublicKey")
	}
	assets, err := readChecksums(*sums)
	if err != nil {
		return err
	}
	if len(assets) == 0 {
		return fmt.Errorf("%s lists no hubridge binaries", *sums)
	}
	body, err := json.MarshalIndent(update.Manifest{Version: *version, Protocol: protocol.Version, Assets: assets}, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if err := os.WriteFile(*out, body, 0o644); err != nil {
		return err
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(key, body)) + "\n"
	if err := os.WriteFile(*out+".sig", []byte(sig), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s and %s.sig: %s, protocol %d, %d binaries\n", filepath.Base(*out), filepath.Base(*out), *version, protocol.Version, len(assets))
	return nil
}

func readChecksums(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	assets := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.Fields(sc.Text())
		if len(parts) != 2 {
			continue
		}
		if name := strings.TrimPrefix(parts[1], "*"); strings.HasPrefix(name, "hubridge-") {
			assets[name] = parts[0]
		}
	}
	return assets, sc.Err()
}
