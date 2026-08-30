package plugin

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyPluginPackageRequiresTrustedEd25519Signature(t *testing.T) {
	root := t.TempDir()
	packagePath := filepath.Join(root, "example.zip")
	if err := os.WriteFile(packagePath, []byte("plugin package"), 0600); err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := hashFile(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(privateKey, digest)
	if err := os.WriteFile(packagePath+signatureFileSuffix, []byte(base64.StdEncoding.EncodeToString(signature)), 0600); err != nil {
		t.Fatal(err)
	}

	packageDigest, signer, err := verifyPluginPackage(packagePath, packagePath+signatureFileSuffix, Options{
		PackageRoot: root, RequireSignature: true, TrustedPublicKeys: []ed25519.PublicKey{publicKey},
	})
	if err != nil {
		t.Fatal(err)
	}
	if packageDigest == "" || signer == "" {
		t.Fatalf("digest=%q signer=%q", packageDigest, signer)
	}

	if err := os.WriteFile(packagePath, []byte("tampered package"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := verifyPluginPackage(packagePath, packagePath+signatureFileSuffix, Options{
		PackageRoot: root, RequireSignature: true, TrustedPublicKeys: []ed25519.PublicKey{publicKey},
	}); err == nil {
		t.Fatal("tampered package signature was accepted")
	}
}

func TestVerifyPluginPackageRejectsMissingRequiredSignature(t *testing.T) {
	root := t.TempDir()
	packagePath := filepath.Join(root, "example.zip")
	if err := os.WriteFile(packagePath, []byte("plugin package"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := verifyPluginPackage(packagePath, packagePath+signatureFileSuffix, Options{
		PackageRoot: root, RequireSignature: true,
	}); err == nil {
		t.Fatal("unsigned package was accepted while signatures are required")
	}
}

func TestParseTrustedPublicKeys(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(publicKey)
	keys, err := ParseTrustedPublicKeys(encoded + ", " + encoded)
	if err != nil || len(keys) != 2 {
		t.Fatalf("keys=%d err=%v", len(keys), err)
	}
	if _, err := ParseTrustedPublicKeys("not-base64"); err == nil {
		t.Fatal("invalid public key was accepted")
	}
}
