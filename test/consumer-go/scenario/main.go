// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

// scenario exercises the generated Go client the way a Terraform/OpenTofu
// provider would. Only for the throwaway test VM on 127.0.0.1.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	opnsense "github.com/pipiobjo/opnsense-api-spec/test/consumer-go/opnsense"
)

var failures int

func check(name string, ok bool, detail string) {
	status := "PASS"
	if !ok {
		status = "FAIL"
		failures++
	}
	fmt.Printf("%s  %-46s %s\n", status, name, detail)
}

func short(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 160 {
		s = s[:160] + "..."
	}
	return s
}

func main() {
	url, key, secret := os.Getenv("OPN_URL"), os.Getenv("OPN_KEY"), os.Getenv("OPN_SECRET")
	if !strings.HasPrefix(url, "https://127.0.0.1:") {
		fmt.Println("refusing: only the local test VM is allowed")
		os.Exit(2)
	}
	// default Go transport (HTTP/2 over TLS), as a provider would use it;
	// certificate checks skipped only for the throwaway VM
	hc := &http.Client{Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, // #nosec G402 -- local test VM
		ForceAttemptHTTP2: true,
	}}
	auth := func(_ context.Context, req *http.Request) error {
		req.SetBasicAuth(key, secret)
		return nil
	}
	c, err := opnsense.NewClientWithResponses(url, opnsense.WithHTTPClient(hc), opnsense.WithRequestEditorFn(auth))
	if err != nil {
		panic(err)
	}
	ctx := context.Background()

	// 1. version detection
	info, err := c.CoreFirmwareInfoGetWithResponse(ctx)
	if err != nil {
		check("version: firmware/info (GET)", false, err.Error())
	} else {
		var v struct {
			ProductVersion string `json:"product_version"`
		}
		jerr := json.Unmarshal(info.Body, &v)
		check("version: firmware/info (GET)", info.StatusCode() == 200 && jerr == nil && v.ProductVersion != "",
			fmt.Sprintf("HTTP %d, %d bytes, product_version=%q, decode=%v", info.StatusCode(), len(info.Body), v.ProductVersion, jerr))
	}
	st, err := c.CoreFirmwareStatusWithResponse(ctx, map[string]any{})
	if err != nil {
		check("version: firmware/status (spec: POST only)", false, err.Error())
	} else {
		check("version: firmware/status (spec: POST only)", st.StatusCode() == 200, fmt.Sprintf("HTTP %d %s", st.StatusCode(), short(st.Body)))
	}

	// 2. read settings (typed)
	get := func() (*opnsense.UnboundSettingsGetResponse, error) { return c.UnboundSettingsGetWithResponse(ctx) }
	g, err := get()
	if err != nil {
		check("settings: get + decode", false, err.Error())
	} else {
		ok := g.JSON200 != nil && g.JSON200.Unbound.General != nil && g.JSON200.Unbound.General.Safesearch != nil
		detail := fmt.Sprintf("HTTP %d", g.StatusCode())
		if ok {
			detail += fmt.Sprintf(", general.safesearch=%q", *g.JSON200.Unbound.General.Safesearch)
		} else {
			detail += " JSON200 not decoded: " + short(g.Body)
		}
		check("settings: get + decode", ok, detail)
	}

	// 3. partial update, built from named types
	on := opnsense.UnboundUnboundGeneralWriteSafesearchN1
	body := opnsense.UnboundSettingsSetJSONRequestBody{Unbound: opnsense.UnboundUnboundWrite{
		General: &opnsense.UnboundUnboundGeneralWrite{Safesearch: &on},
	}}
	raw, _ := json.Marshal(body)
	check("settings: partial body only has set fields", string(raw) == `{"unbound":{"general":{"safesearch":"1"}}}`, string(raw))
	s, err := c.UnboundSettingsSetWithResponse(ctx, body)
	if err != nil {
		check("settings: set", false, err.Error())
	} else {
		res := ""
		if s.JSON200 != nil && s.JSON200.Result != nil {
			res = *s.JSON200.Result
		}
		check("settings: set", res == "saved", fmt.Sprintf("HTTP %d result=%q", s.StatusCode(), res))
	}
	if g, err := get(); err == nil && g.JSON200 != nil && g.JSON200.Unbound.General != nil && g.JSON200.Unbound.General.Safesearch != nil {
		check("settings: read back", string(*g.JSON200.Unbound.General.Safesearch) == "1", fmt.Sprintf("safesearch=%q", *g.JSON200.Unbound.General.Safesearch))
	} else {
		check("settings: read back", false, fmt.Sprint(err))
	}

	// 4. apply
	rc, err := c.UnboundServiceReconfigureWithResponse(ctx)
	if err != nil {
		check("apply: service/reconfigure", false, err.Error())
	} else {
		check("apply: service/reconfigure", rc.StatusCode() == 200, fmt.Sprintf("HTTP %d %s", rc.StatusCode(), short(rc.Body)))
	}

	// 5. entry lifecycle (typed)
	disabled := opnsense.UnboundUnboundDnsblBlocklistWriteEnabled("0")
	add, err := c.UnboundSettingsAddDnsblWithResponse(ctx, opnsense.UnboundSettingsAddDnsblJSONRequestBody{
		Blocklist: opnsense.UnboundUnboundDnsblBlocklistWrite{Description: "functest", Enabled: &disabled},
	})
	uuid := ""
	if err != nil {
		check("entry: add", false, err.Error())
	} else {
		if add.JSON200 != nil && add.JSON200.Uuid != nil {
			uuid = *add.JSON200.Uuid
		}
		check("entry: add", uuid != "", fmt.Sprintf("HTTP %d %s", add.StatusCode(), short(add.Body)))
	}
	if uuid != "" {
		ge, err := c.UnboundSettingsGetDnsblByUuidWithResponse(ctx, uuid)
		if err != nil || ge.JSON200 == nil {
			check("entry: get by uuid + decode", false, fmt.Sprint(err, " ", short(ge.Body)))
		} else {
			b := ge.JSON200.Blocklist
			check("entry: get by uuid + decode", b.Description != nil && *b.Description == "functest", fmt.Sprintf("description=%v", deref(b.Description)))
			if b.Type != nil {
				m, err := b.Type.AsSelectOptions()
				sel := 0
				for _, o := range m {
					if o.Selected != nil && *o.Selected == 1 {
						sel++
					}
				}
				check("entry: select field (SelectMap union)", err == nil && len(m) > 0, fmt.Sprintf("%d options, %d selected, err=%v", len(m), sel, err))
			}
		}
		newDesc := "functest-updated"
		up, err := c.UnboundSettingsSetDnsblByUuidWithResponse(ctx, uuid, opnsense.UnboundSettingsSetDnsblByUuidJSONRequestBody{
			Blocklist: opnsense.UnboundUnboundDnsblBlocklistWrite{Description: newDesc},
		})
		res := ""
		if err == nil && up.JSON200 != nil && up.JSON200.Result != nil {
			res = *up.JSON200.Result
		}
		check("entry: set by uuid", res == "saved", fmt.Sprintf("result=%q err=%v", res, err))
		all := -1
		sr, err := c.UnboundSettingsSearchDnsblGetWithResponse(ctx, &opnsense.UnboundSettingsSearchDnsblGetParams{RowCount: &all})
		if err != nil || sr.JSON200 == nil || sr.JSON200.Rows == nil {
			check("entry: search", false, fmt.Sprint(err))
		} else {
			found := false
			for _, row := range *sr.JSON200.Rows {
				if row.Uuid != nil && *row.Uuid == uuid && row.Description != nil && *row.Description == newDesc {
					found = true
				}
			}
			check("entry: search finds updated entry", found, fmt.Sprintf("%d rows", len(*sr.JSON200.Rows)))
		}
		del, err := c.UnboundSettingsDelDnsblByUuidWithResponse(ctx, uuid)
		res = ""
		if err == nil && del.JSON200 != nil && del.JSON200.Result != nil {
			res = *del.JSON200.Result
		}
		check("entry: delete", res == "deleted", fmt.Sprintf("result=%q err=%v", res, err))
	}

	// 6. validation error
	// an invalid value has to be forced past the typed enum
	invalid := opnsense.UnboundUnboundGeneralWriteStats("maybe")
	bad := opnsense.UnboundSettingsSetJSONRequestBody{Unbound: opnsense.UnboundUnboundWrite{
		General: &opnsense.UnboundUnboundGeneralWrite{Stats: &invalid},
	}}
	v, err := c.UnboundSettingsSetWithResponse(ctx, bad)
	if err != nil {
		check("validation: failed result + messages", false, err.Error())
	} else {
		n := 0
		if v.JSON200 != nil && v.JSON200.Validations != nil {
			n = len(*v.JSON200.Validations)
		}
		res := ""
		if v.JSON200 != nil && v.JSON200.Result != nil {
			res = *v.JSON200.Result
		}
		check("validation: failed result + messages", res == "failed" && n > 0, fmt.Sprintf("result=%q validations=%d", res, n))
	}

	// reset
	off := opnsense.UnboundUnboundGeneralWriteSafesearchN0
	reset := opnsense.UnboundSettingsSetJSONRequestBody{Unbound: opnsense.UnboundUnboundWrite{
		General: &opnsense.UnboundUnboundGeneralWrite{Safesearch: &off},
	}}
	_, _ = c.UnboundSettingsSetWithResponse(ctx, reset)

	fmt.Printf("\n%d failure(s)\n", failures)
	if failures > 0 {
		os.Exit(1)
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
