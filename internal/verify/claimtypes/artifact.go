package claimtypes

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// MaxArtifactBytes is the largest file the verifier will hash: the digest
// streams, so memory stays constant, but the limit bounds the time one claim
// may spend.
const MaxArtifactBytes = 256 << 20

var artifactMarkerRe = regexp.MustCompile(`\[ARTIFACT:(?P<body>[^\]\[]*?)\]`)

// MatchArtifact parses the [ARTIFACT] markers of one line.
//
// An ARTIFACT claim asserts that the named file exists and that its content has
// exactly the claimed SHA-256: the honest form of "the artefact is the one that
// was built".
func MatchArtifact(line string) []engine.Claim {
	var claims []engine.Claim
	for _, match := range artifactMarkerRe.FindAllStringSubmatch(line, -1) {
		path, digest, ok := splitArrow(match[1])
		if !ok {
			continue
		}
		claims = append(claims, engine.Claim{
			Kind:   "artifact",
			Path:   trimQuotes(path),
			SHA256: trimQuotes(digest),
		})
	}
	return claims
}

// CheckArtifact re-derives an [ARTIFACT] claim.
//
// Confirmed only when the file was read completely and its digest equals the
// claimed one. A missing file, a non-regular file and a different digest are
// refutations: absence and deviation are findings, not ignorance. A path that
// escapes the artifact root is a defect, because the claim cannot be bound as
// written.
func CheckArtifact(claim engine.Claim, ctx *engine.Ctx) engine.Verdict {
	pathText := trimQuotes(claim.Path)
	claimed := trimQuotes(claim.SHA256)
	if !sha256Shape.MatchString(claimed) {
		return engine.Unverifiable(fmt.Sprintf("not a sha256 digest: %q", claimed)).
			WithClass(engine.ClassDefect)
	}
	if pathText == "" {
		return engine.Unverifiable("the claim names no file").WithClass(engine.ClassDefect)
	}
	root := ctx.ArtifactRoot
	if root == "" {
		root = ctx.Repo
	}
	if root == "" {
		return engine.Unverifiable(
			"no artifact root: a repository or an explicit artifact root says where the path resolves").
			WithClass(engine.ClassEnvironment)
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil || !isDir(rootReal) {
		return engine.Unverifiable(fmt.Sprintf("artifact root is not a directory: %s", root)).
			WithClass(engine.ClassEnvironment)
	}
	resolved, ok := resolvedUnderRoot(rootReal, pathText)
	if !ok {
		return engine.Unverifiable(fmt.Sprintf(
			"the path escapes the artifact root (or is absolute outside it): %q", pathText)).
			WithClass(engine.ClassDefect)
	}
	command := fmt.Sprintf("sha256 of %s under %s", pathText, root)

	// O_NONBLOCK: a named pipe must not block the verifier; regular files
	// ignore the flag. The descriptor is the only handle used from here on.
	handle, openErr := os.OpenFile(resolved, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if openErr != nil {
		if os.IsNotExist(openErr) {
			return engine.Refuted(fmt.Sprintf("file does not exist: %s", pathText)).WithCommand(command)
		}
		return engine.Unverifiable(fmt.Sprintf("cannot open %q: %v", pathText, openErr)).
			WithCommand(command)
	}
	defer func() { _ = handle.Close() }()

	info, statErr := handle.Stat()
	if statErr != nil {
		return engine.Unverifiable(fmt.Sprintf("cannot stat %q: %v", pathText, statErr)).
			WithCommand(command)
	}
	if !info.Mode().IsRegular() {
		return engine.Refuted(fmt.Sprintf("not a regular file: %s", pathText)).WithCommand(command)
	}
	if info.Size() > MaxArtifactBytes {
		return engine.Unverifiable(fmt.Sprintf(
			"%s is %d bytes, beyond the limit of %d bytes (built-in limit)",
			pathText, info.Size(), MaxArtifactBytes)).
			WithCommand(command).
			WithClass(engine.ClassLimit)
	}

	digest := sha256.New()
	total, copyErr := io.Copy(digest, io.LimitReader(handle, MaxArtifactBytes+1))
	if copyErr != nil {
		return engine.Unverifiable(fmt.Sprintf("cannot read %q: %v", pathText, copyErr)).
			WithCommand(command)
	}
	if total > MaxArtifactBytes {
		// The size check above can go stale for a file that grows while it is
		// read; the running count is the hard bound.
		return engine.Unverifiable(fmt.Sprintf(
			"%s grew beyond the limit of %d bytes while it was hashed (built-in limit)",
			pathText, MaxArtifactBytes)).
			WithCommand(command).
			WithClass(engine.ClassLimit)
	}

	actual := hex.EncodeToString(digest.Sum(nil))
	output := fmt.Sprintf("sha256=%s bytes=%d", actual, total)
	if actual == strings.ToLower(claimed) {
		return engine.Confirmed(fmt.Sprintf("%s has the claimed sha256 (%d bytes)", pathText, total)).
			WithCommand(command).
			WithOutput(output)
	}
	return engine.Refuted(fmt.Sprintf("claimed sha256 %s, %s has %s (%d bytes)",
		strings.ToLower(claimed), pathText, actual, total)).
		WithCommand(command).
		WithOutput(output)
}

// resolvedUnderRoot resolves a path inside root, or reports that it leaves it.
//
// A ".." component is refused before any resolution, so the escape question is
// answered lexically and by realpath: a symlink pointing outside the root fails
// the prefix check even though its own path is lexically clean.
func resolvedUnderRoot(rootReal, pathText string) (string, bool) {
	for _, part := range strings.Split(filepath.ToSlash(pathText), "/") {
		if part == ".." {
			return "", false
		}
	}
	candidate := pathText
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(rootReal, candidate)
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		// A missing file still resolves lexically: the refutation comes from
		// the open, not from the confinement check.
		resolved = filepath.Clean(candidate)
	}
	if resolved != rootReal && !strings.HasPrefix(resolved, rootReal+string(os.PathSeparator)) {
		return "", false
	}
	return resolved, true
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
