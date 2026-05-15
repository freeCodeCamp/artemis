// Command covgate enforces a per-package line-coverage floor on the
// output of `go tool cover -func=coverage.out > coverage.txt`.
//
// Usage:
//
//	go run ./tools/covgate -threshold 80 \
//	    -pkg github.com/freeCodeCamp/artemis/internal/auth \
//	    -pkg github.com/freeCodeCamp/artemis/internal/handler \
//	    coverage.txt
//
// Replaces a fragile inline-awk gate that used to live in
// `.github/workflows/test.yml`. One Go program is easier to test,
// easier to extend (per-pkg thresholds, JSON output, etc.) and
// removes the awk → sed → awk shellish that the old gate used.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "covgate:", err)
		os.Exit(1)
	}
}

// run executes the gate. Separated from main() for testability.
func run(args []string, stdout, stderr io.Writer) error {
	threshold := 80.0
	var pkgs []string
	var input string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-threshold":
			i++
			if i >= len(args) {
				return errors.New("-threshold requires a value")
			}
			n, err := strconv.ParseFloat(args[i], 64)
			if err != nil {
				return fmt.Errorf("invalid -threshold %q: %w", args[i], err)
			}
			threshold = n
		case "-pkg":
			i++
			if i >= len(args) {
				return errors.New("-pkg requires a value")
			}
			pkgs = append(pkgs, args[i])
		case "-h", "--help":
			fmt.Fprintln(stdout, "usage: covgate -threshold N -pkg <importpath> [-pkg ...] coverage.txt")
			return nil
		default:
			if strings.HasPrefix(args[i], "-") {
				return fmt.Errorf("unknown flag %q", args[i])
			}
			if input != "" {
				return fmt.Errorf("only one input path supported, got %q and %q", input, args[i])
			}
			input = args[i]
		}
	}
	if len(pkgs) == 0 {
		return errors.New("at least one -pkg flag is required")
	}
	if input == "" {
		return errors.New("coverage.txt path is required")
	}

	f, err := os.Open(input)
	if err != nil {
		return fmt.Errorf("open %s: %w", input, err)
	}
	defer f.Close()

	pcts, err := parseCoverage(f)
	if err != nil {
		return err
	}

	failed, err := checkThreshold(pcts, threshold, pkgs)
	if err != nil {
		return err
	}

	keys := make([]string, 0, len(pkgs))
	keys = append(keys, pkgs...)
	sort.Strings(keys)
	for _, p := range keys {
		fmt.Fprintf(stdout, "%s: %.2f%% (threshold %.2f%%)\n", p, pcts[p], threshold)
	}

	if len(failed) > 0 {
		for _, f := range failed {
			fmt.Fprintf(stderr, "FAIL: %s = %.2f%% < %.2f%%\n", f.Pkg, f.Got, threshold)
		}
		return fmt.Errorf("%d package(s) below threshold", len(failed))
	}
	return nil
}

// parseCoverage reads `go tool cover -func` output and returns the
// per-package average function coverage percentage.
//
// Each input line has the shape:
//
//	<importpath>/<file.go>:<line>:\t<func>\t<pct>%
//
// Plus a final `total:\t(statements)\t<pct>%` line which is ignored
// (we want per-package numbers, not the global total).
func parseCoverage(r io.Reader) (map[string]float64, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	type acc struct {
		sum   float64
		count int
	}
	pkgAcc := map[string]*acc{}

	lines := strings.Split(string(data), "\n")
	sawAny := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "total:") {
			continue
		}
		// Expect 3 tab-separated columns: <path:line>, <func>, <pct%>
		cols := strings.Split(line, "\t")
		nonEmpty := make([]string, 0, 3)
		for _, c := range cols {
			c = strings.TrimSpace(c)
			if c != "" {
				nonEmpty = append(nonEmpty, c)
			}
		}
		if len(nonEmpty) < 3 {
			return nil, fmt.Errorf("malformed coverage line: %q", line)
		}
		loc := nonEmpty[0]
		pctStr := nonEmpty[len(nonEmpty)-1]

		colon := strings.LastIndex(loc, ":")
		if colon < 0 {
			return nil, fmt.Errorf("malformed coverage line (no ':'): %q", line)
		}
		filePath := loc[:colon]
		// Strip the trailing line number (already removed) — colon is
		// between path and line. filePath has shape pkg/.../file.go.
		// Find another ':' for the line — already handled by LastIndex.
		// Drop up to file.go: re-split.
		// filePath now is "<pkg>/<file>.go:<line>" — wait, we used
		// LastIndex for ':'. coverage.txt format is
		// "<pkg>/<file.go>:<line>:\t<func>...". After splitting by tab
		// the first column is "<pkg>/<file.go>:<line>:". So strip the
		// trailing colon if present, then trim line number.
		filePath = strings.TrimSuffix(filePath, ":")
		// Now filePath is "<pkg>/<file.go>:<line>" — strip line.
		if idx := strings.LastIndex(filePath, ":"); idx >= 0 {
			filePath = filePath[:idx]
		}
		pkgPath := path.Dir(filePath)
		if pkgPath == "." || pkgPath == "/" {
			return nil, fmt.Errorf("malformed coverage line (no package): %q", line)
		}

		pct, err := strconv.ParseFloat(strings.TrimSuffix(pctStr, "%"), 64)
		if err != nil {
			return nil, fmt.Errorf("malformed coverage pct %q: %w", pctStr, err)
		}
		a := pkgAcc[pkgPath]
		if a == nil {
			a = &acc{}
			pkgAcc[pkgPath] = a
		}
		a.sum += pct
		a.count++
		sawAny = true
	}
	if !sawAny {
		return nil, errors.New("no per-function coverage lines found")
	}

	out := make(map[string]float64, len(pkgAcc))
	for k, v := range pkgAcc {
		if v.count > 0 {
			out[k] = v.sum / float64(v.count)
		}
	}
	return out, nil
}

// PkgFail reports a package that fell below the threshold.
type PkgFail struct {
	Pkg string
	Got float64
}

// checkThreshold returns the list of pkgs that fall below threshold.
// An entirely missing pkg is an error (the caller asked for a package
// the coverage report doesn't contain — likely a config typo).
func checkThreshold(pcts map[string]float64, threshold float64, pkgs []string) ([]PkgFail, error) {
	var failed []PkgFail
	for _, p := range pkgs {
		got, ok := pcts[p]
		if !ok {
			return nil, fmt.Errorf("package %q not present in coverage report", p)
		}
		if got < threshold {
			failed = append(failed, PkgFail{Pkg: p, Got: got})
		}
	}
	return failed, nil
}
