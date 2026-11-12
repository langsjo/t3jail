package sshx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestEnsureKeyCreatesUsableKeyOnce(t *testing.T) {
	key := filepath.Join(t.TempDir(), "id_ed25519")
	pub1, err := EnsureKey(key)
	if err != nil {
		t.Fatal(err)
	}
	privPEM, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(privPEM)
	if err != nil {
		t.Fatalf("private key is not OpenSSH-parseable: %v", err)
	}
	parsedPub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pub1))
	if err != nil {
		t.Fatal(err)
	}
	if string(parsedPub.Marshal()) != string(signer.PublicKey().Marshal()) {
		t.Fatal("public key does not match private key")
	}
	if fi, _ := os.Stat(key); fi.Mode().Perm() != 0o600 {
		t.Errorf("private key mode %v", fi.Mode().Perm())
	}

	pub2, err := EnsureKey(key)
	if err != nil || pub2 != pub1 {
		t.Fatalf("second call regenerated or failed: %v", err)
	}
}

func TestEnsureKeyRefusesPublicWithoutPrivate(t *testing.T) {
	key := filepath.Join(t.TempDir(), "id_ed25519")
	os.WriteFile(key+".pub", []byte("ssh-ed25519 AAAA t3jail\n"), 0o644)
	if _, err := EnsureKey(key); err == nil {
		t.Fatal("expected error")
	}
}

func TestRenderConfigQuoting(t *testing.T) {
	cfg := RenderConfig(ConfigParams{
		User:         "agent",
		KeyPath:      "/home/a b/.local/state/t3jail/default/ssh/id_ed25519",
		KnownHosts:   "/s/known_hosts",
		ControlPath:  "/run/user/1000/t3jail/default/ssh-control",
		ProxyCommand: []string{"/nix/store/x-t3jail/bin/t3jail", "__vsock-proxy", "22", "it's"},
	})
	for _, want := range []string{
		`IdentityFile "/home/a b/.local/state/t3jail/default/ssh/id_ed25519"`,
		`ProxyCommand /nix/store/x-t3jail/bin/t3jail __vsock-proxy 22 'it'\''s'`,
		"User agent",
		"StrictHostKeyChecking accept-new",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config lacks %q:\n%s", want, cfg)
		}
	}
}
