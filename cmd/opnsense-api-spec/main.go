// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

// Command opnsense-api-spec builds a versioned API definition for an
// OPNsense release from the official package repository.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pipiobjo/opnsense-api-spec/internal/capability"
	"github.com/pipiobjo/opnsense-api-spec/internal/jsonfile"
	"github.com/pipiobjo/opnsense-api-spec/internal/openapi"
	"github.com/pipiobjo/opnsense-api-spec/internal/pipeline"
	"github.com/pipiobjo/opnsense-api-spec/internal/pkgrepo"
	"github.com/pipiobjo/opnsense-api-spec/internal/sbom"
	"github.com/pipiobjo/opnsense-api-spec/internal/schemacheck"
	"github.com/pipiobjo/opnsense-api-spec/internal/verify"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage: opnsense-api-spec <command> [flags]

commands:
  releases                 list release snapshots on the mirror
  fetch  -release X        download, verify and unpack MVC sources
  build  -release X        fetch + build IR, OpenAPI and coverage into -out
  matrix -series A,B       build every release of the series (with plugins),
                           diffs between consecutive releases, dist/index.json
  diff   -from F -to T     compare two IR files (*.ir.json)
  proxy  -release X [-listen A]
                           serve the package snapshot of release X to an
                           isolated test VM (default listen 127.0.0.1:18080)
  verify -ir F -url U -keyfile K [-write] [-packages a,b|installed]
                           contract tests against a THROWAWAY test instance
                           (only https://127.0.0.1:<port> or localhost);
                           writes *.verified.json and re-renders the OpenAPI
  installed -url U -keyfile K
                           installed packages of the THROWAWAY test instance,
                           one per line (what verify -packages installed uses)
  index                    rebuild dist/index.json (after verify runs)
  capabilities [-out DIR]  build the capability data (catalog.json and one
                           document per package) from dist/ into DIR
                           (default dist/capabilities)
  plan                     print the releases to contract-test as a JSON list
                           (newest release per series, current composition
                           not verified with this generator)
  sbom                     write the SBOM (CycloneDX 1.6) of every release in
                           -dist: the packages its description was derived
                           from (dist/<release>/opnsense-<release>.sbom.cdx.json)
  failures FILE...         print the failing checks of *.verified.json files
                           as Markdown lines (what the run summary and the
                           failure report show; the rule is verify.Status)

flags:
  -mirror URL   package mirror (default https://pkg.opnsense.org)
  -arch A       architecture (default amd64)
  -cache DIR    download cache (default .cache/pkg)
  -out DIR      output directory (fetch: .cache/src/X, build: dist/X)
  -plugins      include all os-* plugins (default: core only)
  -latest       matrix: only the newest release of each series
  -force        matrix: rebuild releases that already exist in dist/
  -offline      use cached catalogs instead of downloading them
  -keys FILE    signing key list, extended through the signed key chain
                (default internal/pkgrepo/keys.json; built-in list if absent)
  -dist DIR     output root of matrix/index, input of capabilities (default dist)
  -series all   matrix: every series from -series-floor (default 25.7) on`)
	os.Exit(2)
}

type opts struct {
	base, arch, cache, release, out string
	series, from, to                string
	irFile, url, keyfile, packages  string
	plugins, latest, force, write   bool
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	var o opts
	fs.StringVar(&o.base, "mirror", pkgrepo.DefaultBaseURL, "")
	fs.StringVar(&o.arch, "arch", "amd64", "")
	fs.StringVar(&o.cache, "cache", ".cache/pkg", "")
	fs.StringVar(&o.release, "release", "", "")
	fs.StringVar(&o.out, "out", "", "")
	fs.BoolVar(&o.plugins, "plugins", false, "")
	fs.StringVar(&o.series, "series", "", "")
	fs.StringVar(&o.from, "from", "", "")
	fs.StringVar(&o.to, "to", "", "")
	fs.BoolVar(&o.latest, "latest", false, "")
	fs.BoolVar(&o.force, "force", false, "")
	fs.StringVar(&o.irFile, "ir", "", "")
	fs.StringVar(&o.url, "url", "", "")
	fs.StringVar(&o.keyfile, "keyfile", "", "")
	fs.StringVar(&o.packages, "packages", "opnsense", "")
	fs.BoolVar(&o.write, "write", false, "")
	offline := fs.Bool("offline", false, "")
	listen := fs.String("listen", "127.0.0.1:18080", "")
	keysPath := fs.String("keys", "internal/pkgrepo/keys.json", "")
	floor := fs.String("series-floor", "25.7", "")
	accepted := fs.String("accepted", "verify/accepted.json", "")
	distDir := fs.String("dist", "dist", "")
	fs.Usage = usage
	_ = fs.Parse(args)
	c := pkgrepo.NewClient(o.base, o.cache)
	c.Offline = *offline
	keys := pkgrepo.Keys
	if *keysPath != "" {
		if k, err := pkgrepo.LoadKeys(*keysPath); err == nil {
			keys = k
		} else if !os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	}
	pl := &pipeline.Pipeline{Client: c, Keys: keys, Arch: o.arch, Dist: *distDir,
		SrcRoot: filepath.Join(".cache", "src"), Log: os.Stderr, KeysPath: *keysPath, SeriesFloor: *floor}

	var err error
	switch cmd {
	case "releases":
		err = cmdReleases(c, o.arch)
	case "fetch":
		if o.release == "" {
			usage()
		}
		_, err = pl.Fetch(o.release, o.plugins, o.out)
	case "build":
		if o.release == "" {
			usage()
		}
		_, err = pl.Build(o.release, o.plugins, o.out)
	case "matrix":
		if o.series == "" {
			usage()
		}
		var outs []pipeline.Outcome
		outs, err = pl.Matrix(strings.Split(o.series, ","), o.latest, o.force)
		for _, oc := range outs {
			note := ""
			if oc.Quality != nil && oc.Quality.Notable() {
				note = fmt.Sprintf("  (%d regression(s), %d framework file(s) changed)",
					len(oc.Quality.Regressions), len(oc.Quality.FrameworkChanged))
			}
			fmt.Fprintf(os.Stderr, "%-10s %-11s %-32s %-11s%s\n", oc.Release, oc.Action, oc.Composition, oc.Status, note)
		}
		// one line per problem, "warning:"/"error:" prefixed (CI turns them
		// into annotations)
		for _, oc := range outs {
			switch {
			case oc.Kept, oc.Action == pipeline.ActionUnavailable:
				fmt.Fprintf(os.Stderr, "warning: %s %s: %s\n", oc.Release, oc.Action, oc.Reason)
			case oc.Action == pipeline.ActionFailed:
				fmt.Fprintf(os.Stderr, "error: %s build failed: %s\n", oc.Release, oc.Reason)
			}
		}
	case "diff":
		if o.from == "" || o.to == "" {
			usage()
		}
		err = cmdDiff(o)
	case "verify":
		if o.irFile == "" || o.url == "" || o.keyfile == "" {
			usage()
		}
		err = cmdVerify(o, accepted)
	case "installed":
		if o.url == "" || o.keyfile == "" {
			usage()
		}
		err = cmdInstalled(o)
	case "index":
		err = cmdIndex(pl.Dist, *accepted)
	case "capabilities":
		dir := o.out
		if dir == "" {
			dir = filepath.Join(pl.Dist, "capabilities")
		}
		err = cmdCapabilities(pl.Dist, dir)
	case "failures":
		err = cmdFailures(fs.Args())
	case "sbom":
		err = cmdSBOM(pl.Dist)
	case "plan":
		var idx *pipeline.Index
		if idx, err = pipeline.ReadIndex(filepath.Join(pl.Dist, "index.json")); err == nil {
			err = json.NewEncoder(os.Stdout).Encode(idx.VerifyPlan())
		}
	case "proxy":
		if o.release == "" {
			usage()
		}
		err = proxy(c, o.arch, o.release, *listen)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func cmdReleases(c *pkgrepo.Client, arch string) error {
	rs, err := c.Discover(arch)
	if err != nil {
		return err
	}
	for _, r := range rs {
		fmt.Printf("%-10s %-6s %s\n", r.Version, r.Series, r.ABI)
	}
	return nil
}

// vmClient connects to the throwaway test instance (loopback only) with
// the API key file tools/testvm.sh wrote.
func vmClient(o opts) (*verify.Client, error) {
	raw, err := os.ReadFile(o.keyfile)
	if err != nil {
		return nil, err
	}
	var key struct{ Key, Secret string }
	if err := json.Unmarshal(raw, &key); err != nil || key.Key == "" {
		return nil, fmt.Errorf("%s: expected {\"key\": ..., \"secret\": ...}", o.keyfile)
	}
	c, err := verify.NewClient(o.url, key.Key, key.Secret, []string{"127.0.0.1", "localhost"})
	if err != nil {
		return nil, err
	}
	// software emulation is several times slower; tools/testvm.sh exports
	// its factor so a slow instance does not turn into failed checks
	if s := os.Getenv("TESTVM_SLOW"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("TESTVM_SLOW=%q: want a whole number of at least 1", s)
		}
		c.ScaleTimeouts(n)
		if n == 1 {
			return c, nil
		}
		fmt.Fprintf(os.Stderr, "request time limit %s (TESTVM_SLOW=%d)\n", verify.RequestTimeout*time.Duration(n), n)
	}
	return c, nil
}

// cmdInstalled prints the installed packages of the test instance, one per
// line, sorted: the same detection the contract test uses, for testvm.sh.
func cmdInstalled(o opts) error {
	c, err := vmClient(o)
	if err != nil {
		return err
	}
	pk, err := c.InstalledPackages()
	if err != nil {
		return err
	}
	for _, p := range slices.Sorted(maps.Keys(pk)) {
		fmt.Println(p)
	}
	return nil
}

// cmdSBOM writes the SBOM of every release in dist from its IR. The file
// is deterministic, so an unchanged release keeps an unchanged file.
func cmdSBOM(dist string) error {
	idx, err := pipeline.ReadIndex(filepath.Join(dist, "index.json"))
	if err != nil {
		return err
	}
	for _, r := range idx.Releases {
		rel, err := pipeline.ReadIR(filepath.Join(dist, r.Release, "opnsense-"+r.Release+".ir.json"))
		if err != nil {
			return err
		}
		out, err := sbom.ForRelease(rel)
		if err != nil {
			return fmt.Errorf("%s: %w", r.Release, err)
		}
		if err := os.WriteFile(filepath.Join(dist, r.Release, sbom.FileName(r.Release)), out, 0o644); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "sbom: %d releases\n", len(idx.Releases))
	return nil
}

func cmdVerify(o opts, accepted *string) error {
	rel, err := pipeline.ReadIR(o.irFile)
	if err != nil {
		return err
	}
	c, err := vmClient(o)
	if err != nil {
		return err
	}
	// validate responses against the document generated clients use
	plain, _ := openapi.Render(rel)
	if c.Schema, err = schemacheck.New(plain); err != nil {
		return fmt.Errorf("schema checker: %w", err)
	}
	pk := map[string]bool{}
	if o.packages == "installed" {
		if pk, err = c.InstalledPackages(); err != nil {
			return err
		}
	} else {
		for _, p := range strings.Split(o.packages, ",") {
			pk[strings.TrimSpace(p)] = true
		}
	}
	fmt.Fprintf(os.Stderr, "testing endpoints of %d installed/selected packages\n", len(pk))
	rep := verify.Run(c, rel, pk, o.write)
	if acc, err := verify.LoadAccepted(*accepted); err == nil {
		if n := verify.ApplyAccepted(rep, acc); n > 0 {
			fmt.Fprintf(os.Stderr, "%d finding(s) accepted per %s\n", n, *accepted)
		}
		for _, id := range rep.UnusedAccepted {
			fmt.Fprintf(os.Stderr, "accepted entry unused on %s (candidate for removal): %s\n", rep.Release, id)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	// record which catalog state was tested (dist/<release>/composition.json)
	if comp, err := pipeline.ReadComposition(filepath.Join(filepath.Dir(o.irFile), "composition.json")); err == nil {
		rep.Composition = comp.ID()
	}
	if !strings.HasPrefix(rep.Instance, rel.Version) {
		fmt.Fprintf(os.Stderr, "warning: instance runs %q, IR is %s\n", rep.Instance, rel.Version)
	}
	base := strings.TrimSuffix(o.irFile, ".ir.json")
	if err := jsonfile.Write(base+".verified.json", rep); err != nil {
		return err
	}
	counts := map[string]int{}
	for _, r := range rep.Results {
		counts[r.Key()]++
	}
	for _, k := range pipeline.SortedKeys(counts) {
		fmt.Fprintf(os.Stderr, "%-24s %d\n", k, counts[k])
	}
	doc, _ := openapi.RenderVerified(rel, rep)
	if err := jsonfile.Write(base+".openapi.json", doc); err != nil {
		return err
	}
	// a failed contract check must fail the command (and CI), not only
	// show up in the counts; accepted findings are no longer mismatch/error
	if n := failures(rep); n > 0 {
		return fmt.Errorf("%d contract check(s) failed; see %s.verified.json", n, base)
	}
	return nil
}

// cmdIndex rebuilds dist/index.json and records the accepted entries that
// no newest contract test needed (the watchdog's "unused" condition).
func cmdIndex(dist, accepted string) error {
	if err := pipeline.BuildIndex(dist); err != nil {
		return err
	}
	list, err := verify.LoadAccepted(accepted)
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	path := filepath.Join(dist, "index.json")
	idx, err := pipeline.ReadIndex(path)
	if err != nil {
		return err
	}
	idx.SetUnusedAccepted(list)
	return jsonfile.Write(path, idx)
}

func failures(rep *verify.Report) int { return len(rep.Failures()) }

// cmdFailures prints one Markdown line per failing check, with at most
// three findings.
func cmdFailures(files []string) error {
	if len(files) == 0 {
		usage()
	}
	for _, f := range files {
		var rep verify.Report
		if err := jsonfile.Read(f, &rep); err != nil {
			return err
		}
		for _, r := range rep.Failures() {
			fs := r.Findings
			if len(fs) > 3 {
				fs = fs[:3]
			}
			fmt.Printf("- %s `%s` (%s): %s\n", r.Check, r.Path, r.Status, strings.Join(fs, "; "))
		}
	}
	return nil
}

func cmdDiff(o opts) error {
	a, err := pipeline.ReadIR(o.from)
	if err != nil {
		return err
	}
	b, err := pipeline.ReadIR(o.to)
	if err != nil {
		return err
	}
	dir := o.out
	if dir == "" {
		dir = filepath.Join("dist", "diffs")
	}
	r, err := pipeline.WriteDiff(a, b, dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%s -> %s: %d package, %d model, %d field, %d endpoint changes\n",
		a.Version, b.Version, len(r.Packages), len(r.Models), len(r.Fields), len(r.Endpoints))
	return nil
}

func cmdCapabilities(dist, dir string) error {
	cat, docs, err := capability.Build(dist)
	if err != nil {
		return err
	}
	if err := capability.Write(dir, cat, docs); err != nil {
		return err
	}
	n := 0
	for _, d := range docs {
		n += len(d.Resources)
	}
	fmt.Fprintf(os.Stderr, "%d packages, %d resources, %d core versions -> %s\n", len(docs), n, len(cat.Cores), dir)
	return nil
}
