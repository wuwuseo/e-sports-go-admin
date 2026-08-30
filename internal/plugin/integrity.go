package plugin

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	signatureFileSuffix      = ".sig"
	integrityFileName        = ".go-admin-integrity.json"
	integrityFormatVersion   = 1
	maxSignatureFileSize     = 4096
	maxIntegrityMetadataSize = 2 << 20
)

type integrityMetadata struct {
	Format        int               `json:"format"`
	PackageSHA256 string            `json:"package_sha256"`
	Signer        string            `json:"signer,omitempty"`
	Files         map[string]string `json:"files"`
}

func ParseTrustedPublicKeys(value string) ([]ed25519.PublicKey, error) {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == ' ' || r == '\t'
	})
	keys := make([]ed25519.PublicKey, 0, len(fields))
	for _, field := range fields {
		decoded, err := base64.StdEncoding.DecodeString(field)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("invalid Ed25519 public key")
		}
		keys = append(keys, ed25519.PublicKey(append([]byte(nil), decoded...)))
	}
	return keys, nil
}

func verifyPluginPackage(packagePath, signaturePath string, opts Options) (string, string, error) {
	digest, err := hashFile(packagePath)
	if err != nil {
		return "", "", err
	}
	signature, found, err := readDetachedSignature(signaturePath, opts.PackageRoot)
	if err != nil {
		return "", "", err
	}
	if !found {
		if opts.RequireSignature {
			return "", "", errors.New("plugin signature is required")
		}
		return hex.EncodeToString(digest), "", nil
	}
	if len(opts.TrustedPublicKeys) == 0 {
		return "", "", errors.New("plugin signature has no trusted public key")
	}
	for _, publicKey := range opts.TrustedPublicKeys {
		if len(publicKey) != ed25519.PublicKeySize {
			continue
		}
		if ed25519.Verify(publicKey, digest, signature) {
			fingerprint := sha256.Sum256(publicKey)
			return hex.EncodeToString(digest), hex.EncodeToString(fingerprint[:8]), nil
		}
	}
	return "", "", errors.New("plugin signature verification failed")
}

func readDetachedSignature(path, packageRoot string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxSignatureFileSize {
		return nil, false, errors.New("invalid plugin signature file")
	}
	resolvedRoot, err := filepath.EvalSymlinks(packageRoot)
	if err != nil {
		return nil, false, err
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil || !isPathWithinRoot(resolvedRoot, resolvedPath) {
		return nil, false, errors.New("plugin signature escapes package root")
	}
	encoded, err := os.ReadFile(resolvedPath)
	if err != nil {
		return nil, false, err
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil || len(signature) != ed25519.SignatureSize {
		return nil, false, errors.New("invalid plugin signature")
	}
	return signature, true, nil
}

func writeIntegrityMetadata(root, packageDigest, signer string) error {
	files, err := hashPluginFiles(root)
	if err != nil {
		return err
	}
	metadata := integrityMetadata{
		Format: integrityFormatVersion, PackageSHA256: packageDigest, Signer: signer, Files: files,
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(root, integrityFileName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func verifyPluginIntegrity(root string) error {
	metadataPath := filepath.Join(root, integrityFileName)
	info, err := os.Lstat(metadataPath)
	if err != nil {
		return fmt.Errorf("read plugin integrity metadata: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxIntegrityMetadataSize {
		return errors.New("invalid plugin integrity metadata file")
	}
	resolvedPath, err := filepath.EvalSymlinks(metadataPath)
	if err != nil || !isPathWithinRoot(root, resolvedPath) {
		return errors.New("invalid plugin integrity metadata path")
	}
	encoded, err := os.ReadFile(resolvedPath)
	if err != nil {
		return fmt.Errorf("read plugin integrity metadata: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var metadata integrityMetadata
	if err := decoder.Decode(&metadata); err != nil {
		return fmt.Errorf("decode plugin integrity metadata: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("invalid trailing plugin integrity metadata")
	}
	if metadata.Format != integrityFormatVersion || metadata.PackageSHA256 == "" || len(metadata.Files) == 0 {
		return errors.New("invalid plugin integrity metadata")
	}
	actualFiles, err := hashPluginFiles(root)
	if err != nil {
		return err
	}
	if len(actualFiles) != len(metadata.Files) {
		return errors.New("plugin file set integrity mismatch")
	}
	for name, want := range metadata.Files {
		if name == "" || name == integrityFileName || filepath.IsAbs(name) || filepath.ToSlash(filepath.Clean(name)) != name || strings.HasPrefix(name, "../") {
			return fmt.Errorf("invalid integrity path %q", name)
		}
		got, found := actualFiles[name]
		if !found || !strings.EqualFold(got, want) {
			return fmt.Errorf("plugin file integrity mismatch %q", name)
		}
	}
	return nil
}

func hashPluginFiles(root string) (map[string]string, error) {
	files := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported plugin file %q", path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		if name == integrityFileName {
			return nil
		}
		digest, err := hashFile(path)
		if err != nil {
			return err
		}
		files[name] = hex.EncodeToString(digest)
		return nil
	})
	return files, err
}

func hashFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return nil, err
	}
	return hash.Sum(nil), nil
}

func isPathWithinRoot(root, target string) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absTarget, err := filepath.Abs(target)
	if err != nil || absTarget == absRoot {
		return false
	}
	resolvedRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return false
	}
	resolvedTarget, err := filepath.EvalSymlinks(absTarget)
	if err != nil || resolvedTarget == resolvedRoot {
		return false
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedTarget)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
