package engine

import (
	"fmt"
	"os"
)

// Checkout is a throwaway clone of the inspected repository at one commit.
//
// The clone is created with --no-hardlinks so the run cannot write through to
// the object store it was cloned from, and it is removed when the caller
// closes it.
type Checkout struct {
	Dir string
	// Prefix is how a verdict shows the way into this directory.
	Prefix string
}

// NewCheckout clones the repository and checks out commit. A failure returns
// the verdict to report and no checkout.
func (c *Ctx) NewCheckout(label, commit string) (*Checkout, *Verdict) {
	if err := os.MkdirAll(c.TmpDir, 0o755); err != nil {
		verdict := Unverifiable(fmt.Sprintf("cannot create a working directory under %s: %v", c.TmpDir, err)).
			WithClass(ClassEnvironment)
		return nil, &verdict
	}
	dir, err := os.MkdirTemp(c.TmpDir, label+"-")
	if err != nil {
		verdict := Unverifiable(fmt.Sprintf("cannot create a throwaway checkout under %s: %v", c.TmpDir, err)).
			WithClass(ClassEnvironment)
		return nil, &verdict
	}

	prefix := fmt.Sprintf("git clone --no-hardlinks <repo> <checkout> && git checkout %s", commit)
	clone := runGit(c.Context(), []string{
		"git",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		// A repository path that reads as an option must not reach the argument
		// list; "--" ends the options.
		"clone", "--quiet", "--no-hardlinks", "--", c.Repo, dir,
	}, gitTimeout)
	if clone.Code != 0 {
		_ = os.RemoveAll(dir)
		verdict := Unverifiable("could not create a clean checkout").
			WithCommand(prefix).
			WithOutput(clone.Output())
		return nil, &verdict
	}
	checkedOut := runGit(c.Context(), []string{
		"git",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		"-C", dir, "checkout", "--quiet", commit,
	}, gitTimeout)
	if checkedOut.Code != 0 {
		_ = os.RemoveAll(dir)
		verdict := Unverifiable(fmt.Sprintf("could not check out %s", commit)).
			WithCommand(prefix).
			WithOutput(checkedOut.Output())
		return nil, &verdict
	}
	return &Checkout{Dir: dir, Prefix: prefix}, nil
}

// Close removes the throwaway checkout.
func (c *Checkout) Close() {
	_ = os.RemoveAll(c.Dir)
}
