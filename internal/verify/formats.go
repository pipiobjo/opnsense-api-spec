// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package verify

import (
	"strings"

	"github.com/pipiobjo/opnsense-api-spec/internal/model"
)

// vector is one probe of a format: a value and the verdict the format's
// definition (docs/capabilities.md, section Formats) gives for it.
type vector struct {
	claim  Claim
	value  string
	accept bool
}

// Documentation addresses (RFC 5737, RFC 3849) and names (RFC 2606).
const (
	addr4 = "192.0.2.10"
	addr6 = "2001:db8::10"
	net4  = "192.0.2.0/24"
	net6  = "2001:db8::/64"
	host1 = "ct-1.example.com"
	host2 = "ct-2.example.com"
)

// formatVectors: for each rule of the format a value that it decides, and
// values on the edges where PHP's checks and common libraries differ
// (zone indices, leading zeros, MAC addresses of other lengths).
func formatVectors(f *model.Format, list *model.List) []vector {
	var out []vector
	add := func(c Claim, v string, accept bool) { out = append(out, vector{c, v, accept}) }
	switch f.Kind {
	case model.FormatNetwork:
		v4, v6 := f.Family != "ipv6", f.Family != "ipv4"
		addrOK := f.Netmask != model.NetmaskRequired
		netOK := f.Netmask != model.NetmaskForbidden
		a, n := addr4, net4 // values of a family the field takes
		if !v4 {
			a, n = addr6, net6
		}
		if addrOK {
			add(ClaimFamily, addr4, v4)
			add(ClaimFamily, addr6, v6)
		} else {
			add(ClaimFamily, net4, v4)
			add(ClaimFamily, net6, v6)
		}
		switch f.Netmask {
		case model.NetmaskForbidden:
			add(ClaimNetmask, n, false)
		case model.NetmaskAllowed:
			add(ClaimNetmask, n, true)
		case model.NetmaskRequired:
			add(ClaimNetmask, a, false)
		}
		if netOK {
			add(ClaimStrict, a+strings.TrimPrefix(n, strings.Split(n, "/")[0]), !f.Strict) // address bits after the prefix
			if v4 {
				add(ClaimFormat, "192.0.2.0/33", false)
				add(ClaimFormat, "192.0.2.0/+24", false)
			} else {
				add(ClaimFormat, "2001:db8::/129", false)
			}
		}
		add(ClaimWildcard, "any", f.Any)
		if addrOK {
			if v4 {
				add(ClaimFormat, "192.0.2.256", false)
				add(ClaimFormat, "192.0.2.010", false) // leading zero
			}
			if v6 {
				add(ClaimFormat, "fe80::1%em0", false) // zone index
				add(ClaimFormat, "2001:db8::1:2:3:4:5:6:7", false)
			}
			if list != nil {
				other := strings.Replace(a, "10", "11", 1)
				add(ClaimFormat, a+list.Separator+other, true)
				add(ClaimFormat, a+list.Separator+"contract-test", false)
			}
		}
		add(ClaimFormat, "contract-test", false)
	case model.FormatHostname:
		add(ClaimFormat, host1, true)
		add(ClaimFormat, "ct 1.example.com", false)
		add(ClaimFormat, "ct/1.example.com", false)
		add(ClaimIPAllowed, addr4, f.IP)
		add(ClaimIPAllowed, addr6, f.IP)
		add(ClaimDNSName, "_ct.example.com", f.DNSName)
		add(ClaimHostWildcard, "*", f.HostWildcard)
		add(ClaimDomainWildcard, "*.example.com", f.DomainWildcard)
		// "@" is also a DNS name (it has no space, slash or star)
		add(ClaimZoneRoot, "@", f.ZoneRoot || f.DNSName)
		if list != nil {
			add(ClaimFormat, host1+list.Separator+host2, true)
			add(ClaimFormat, "ct 1"+list.Separator+host2, false)
		}
	case model.FormatMAC:
		add(ClaimFormat, "02:00:00:00:00:01", true)
		add(ClaimFormat, "02-00-00-00-00-01", true)
		add(ClaimFormat, "0200.0000.0001", true)
		add(ClaimFormat, "02:00:00:00:00", false)
		add(ClaimFormat, "02:00:00:00:00:00:00:01", false) // EUI-64
		add(ClaimFormat, "2:0:0:0:0:1", false)
		add(ClaimFormat, "02:00:00:00:00:0g", false)
		if list != nil {
			add(ClaimFormat, "02:00:00:00:00:01"+list.Separator+"02:00:00:00:00:02", true)
			add(ClaimFormat, "02:00:00:00:00:01"+list.Separator+"02:00", false)
		}
	case model.FormatIPPort:
		v4, v6 := f.Family != "ipv6", f.Family != "ipv4"
		add(ClaimFamily, addr4+":53", v4 || f.Hostname) // an IPv4 address is also a host name
		add(ClaimFamily, "["+addr6+"]:53", v6)
		add(ClaimPortOptional, addr4, f.PortOptional && (v4 || f.Hostname))
		add(ClaimPortOptional, addr6, f.PortOptional && v6)
		add(ClaimHostname, host1+":53", f.Hostname)
		if v4 || f.Hostname {
			add(ClaimFormat, addr4+":0", false)
			add(ClaimFormat, addr4+":65536", false)
			add(ClaimFormat, addr4+":053", false) // leading zero
		}
		if v6 {
			add(ClaimFormat, "["+addr6+"]:65536", false)
		}
		if list != nil && (v4 || f.Hostname) {
			add(ClaimFormat, addr4+":53"+list.Separator+"192.0.2.11:53", true)
			add(ClaimFormat, addr4+":53"+list.Separator+"192.0.2.11:0", false)
		}
	case model.FormatPort:
		add(ClaimFormat, "443", true)
		add(ClaimFormat, "0", false)
		add(ClaimFormat, "65536", false)
		add(ClaimFormat, "0443", false) // the options are the numbers as written by PHP
		add(ClaimWildcard, "any", f.Any)
		for _, sv := range f.Services {
			add(ClaimService, sv, true)
		}
		if len(f.Services) > 0 && list == nil {
			// one name is taken in any case and stored in lower case
			add(ClaimService, strings.ToUpper(f.Services[len(f.Services)-1]), true)
		}
		add(ClaimRange, "1000-2000", f.Ranges)
		if f.Ranges {
			add(ClaimRange, "2000-1000", false)
			add(ClaimRange, "0-80", false)
			add(ClaimRange, "1000-1000", false)
			add(ClaimRange, "1000- 2000", f.RangeSpaces)
		}
		if !f.Aliases {
			// with aliases, a name is the firewall's to judge
			add(ClaimFormat, "contract-test", false)
		}
		if list != nil {
			add(ClaimFormat, "80"+list.Separator+"443", true)
			add(ClaimFormat, "80"+list.Separator+"0", false)
		}
	case model.FormatURL:
		add(ClaimFormat, "https://example.com/contract-test?x=1", true)
		add(ClaimFormat, "http://192.0.2.10:8080/", true)
		add(ClaimFormat, "https://[2001:db8::10]/", true)
		add(ClaimFormat, "ftp://example.com/", true)
		add(ClaimFormat, "mailto:ct@example.com", true)
		add(ClaimFormat, "example.com", false)
		add(ClaimFormat, "https://", false)
		add(ClaimFormat, "https://ex ample.com/", false)
		add(ClaimFormat, "https://-ct.example.com/", false)
		add(ClaimFormat, "https://example.com/ä", false)
	}
	return out
}
