// Command opnsense-api-spec builds a versioned API definition for an
// OPNsense release from the official package repository.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pipiobjo/opnsense-api-spec/internal/openapi"
	"github.com/pipiobjo/opnsense-api-spec/internal/pipeline"
	"github.com/pipiobjo/opnsense-api-spec/internal/pkgrepo"
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
	pl := &pipeline.Pipeline{Client: c, Keys: keys, Arch: o.arch, Dist: "dist",
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

func cmdVerify(o opts, accepted *string) error {
	rel, err := pipeline.ReadIR(o.irFile)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(o.keyfile)
	if err != nil {
		return err
	}
	var key struct{ Key, Secret string }
	if err := json.Unmarshal(raw, &key); err != nil || key.Key == "" {
		return fmt.Errorf("%s: expected {\"key\": ..., \"secret\": ...}", o.keyfile)
	}
	c, err := verify.NewClient(o.url, key.Key, key.Secret, []string{"127.0.0.1", "localhost"})
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
	if err := pipeline.WriteJSON(base+".verified.json", rep); err != nil {
		return err
	}
	counts := map[string]int{}
	for _, r := range rep.Results {
		counts[r.Check+"/"+r.Status]++
	}
	for _, k := range pipeline.SortedKeys(counts) {
		fmt.Fprintf(os.Stderr, "%-24s %d\n", k, counts[k])
	}
	doc, _ := openapi.RenderVerified(rel, rep)
	return pipeline.WriteJSON(base+".openapi.json", doc)
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
