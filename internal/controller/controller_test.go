// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package controller

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func load(t *testing.T) map[string]*Controller {
	t.Helper()
	classes := map[string]*Class{}
	for _, f := range []string{"Base.php", "SettingsController.php", "ServiceController.php"} {
		src, err := os.ReadFile("testdata/" + f)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range ParseFile(string(src), f) {
			classes[strings.ToLower(c.Name)] = c
		}
	}
	out := map[string]*Controller{}
	for _, c := range Resolve(classes) {
		out[c.Module+"/"+c.Name] = c
	}
	return out
}

func action(c *Controller, name string) *Action {
	for _, a := range c.Actions {
		if a.Name == name {
			return a
		}
	}
	return nil
}

func TestResolveSettings(t *testing.T) {
	cs := load(t)
	c := cs["demo/settings"]
	if c == nil {
		t.Fatalf("demo/settings not found, have %v", cs)
	}
	if c.ModelName != "demo" || c.ModelClass != `OPNsense\Demo\Demo` {
		t.Errorf("model: %q %q", c.ModelName, c.ModelClass)
	}
	if len(c.Gaps) != 0 {
		t.Errorf("unexpected gaps: %v", c.Gaps)
	}
	if action(c, "hidden") != nil {
		t.Error("action inside a # comment must be ignored")
	}
	if a := action(c, "set"); a == nil || a.DeclaredIn != `OPNsense\Base\ApiMutableModelControllerBase` || !a.ChecksPost {
		t.Errorf("inherited set: %+v", a)
	}
	g := action(c, "get")
	if g == nil || !g.CallsParent || g.Overrides != `OPNsense\Base\ApiMutableModelControllerBase` {
		t.Errorf("overridden get: %+v", g)
	}
	gh := action(c, "getHost")
	if gh == nil || len(gh.Params) != 1 || gh.Params[0] != (Param{Name: "uuid", Optional: true}) {
		t.Errorf("getHost params: %+v", gh)
	}
	if !reflect.DeepEqual(gh.Calls[0], BaseCall{Func: "getBase", Key: "host", Path: "hosts.host", Direct: true}) {
		t.Errorf("getHost call: %+v", gh.Calls)
	}
	if a := action(c, "addHost"); a.Calls[0].Key != "host" || a.Calls[0].Path != "hosts.host" {
		t.Errorf("double-quoted literals: %+v", a.Calls)
	}
	if a := action(c, "delHost"); len(a.Calls) != 2 || a.Calls[1].Path != "hosts.host" {
		t.Errorf("delHost calls: %+v", a.Calls)
	}
	if a := action(c, "getPac"); a.Calls[0].Outer != "pac" || a.Calls[0].Key != "match" {
		t.Errorf("outer wrapper: %+v", a.Calls)
	}
	if a := action(c, "getHost"); a.Calls[0].Outer != "" || !a.Calls[0].Direct {
		t.Errorf("no outer wrapper expected: %+v", a.Calls)
	}
	if a := action(c, "dyn"); a.Calls[0].Raw == "" {
		t.Errorf("non-literal argument must be kept raw: %+v", a.Calls)
	}
	if a := action(c, "export"); !a.ChecksGet || a.ChecksPost || len(a.Calls) != 0 {
		t.Errorf("export: %+v", a)
	}
}

func TestResolveService(t *testing.T) {
	c := load(t)["demo/service"]
	if c == nil || action(c, "reconfigure") == nil || action(c, "status") == nil {
		t.Fatalf("service actions missing: %+v", c)
	}
}

func TestSnake(t *testing.T) {
	for in, want := range map[string]string{
		"CtrlAgent": "ctrl_agent", "Dhcpv4": "dhcpv4", "Leases4": "leases4",
		"DNat": "d_nat", "HasyncStatus": "hasync_status", "IDS": "ids", "Settings": "settings",
	} {
		if got := snake(in); got != want {
			t.Errorf("snake(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOverlay(t *testing.T) {
	lagg := `
        $overlay = [];
        $ifnames = [];
        foreach ($this->getModel()->lagg->iterateItems() as $node) {
            $ifnames[] = (string)$node->laggif;
        }
        for ($i = 0; true; ++$i) {
            $laggif = sprintf('lagg%d', $i);
            if (!in_array($laggif, $ifnames)) {
                $overlay['laggif'] = $laggif;
                break;
            }
        }
        `
	laggSet := `
        $node = $this->getModel()->getNodeByReference('lagg.' . $uuid);
        $overlay = null;
        if (!empty($node)) {
            $overlay['laggif'] = (string)$node->laggif;
        }
        `
	cases := []struct {
		call   string
		args   []string
		before string
		want   []Overlay
	}{
		{"addBase", []string{"'domain'", "'domains.domain'", "['type' => 'primary']"}, "", []Overlay{{Field: "type", Value: "primary", Literal: true}}},
		{"addBase", []string{"'dot'", "'dots.dot'", `[ "type" => $this->type ]`}, "", []Overlay{{Field: "type"}}},
		{"setBase", []string{"'vlan'", "'vlan'", "$uuid", "['vlanif' => $new_vlanif]"}, "", []Overlay{{Field: "vlanif"}}},
		{"addBase", []string{"'a'", "'b'", "array('x' => '1', 'y' => \"2\")"}, "", []Overlay{{Field: "x", Value: "1", Literal: true}, {Field: "y", Value: "2", Literal: true}}},
		{"addBase", []string{"'a'", "'b'", "$overlay"}, "", []Overlay{{}}},
		{"addBase", []string{"'errorpage'", "'errorpage'", "array('pagecontent' => base64_encode($this->request->getPost('errorpage')['pagecontent']))"}, "", []Overlay{{Field: "pagecontent", FromRequest: true}}},
		{"addBase", []string{"'a'", "'b'", "null"}, "", nil},
		{"addBase", []string{"'a'", "'b'"}, "", nil},
		{"setBase", []string{"'a'", "'b'", "['x' => 1]"}, "", nil}, // third argument of setBase is the uuid
		{"getBase", []string{"'a'", "'b'", "['x' => '1']"}, "", nil},
		// a variable assembled before the call (interface controllers)
		{"addBase", []string{"'lagg'", "'lagg'", "$overlay"}, lagg, []Overlay{{Field: "laggif"}}},
		{"setBase", []string{"'lagg'", "'lagg'", "$uuid", "$overlay"}, laggSet, []Overlay{{Field: "laggif"}}},
		{"addBase", []string{"'a'", "'b'", "$o"}, "$o = ['type' => 'x'];\n$o['n'] = 'y';", []Overlay{{Field: "type", Value: "x", Literal: true}, {Field: "n", Value: "y", Literal: true}}},
		{"addBase", []string{"'a'", "'b'", "$o"}, "$o = $this->defaults();\n$o['n'] = 'y';", []Overlay{{}}},
		{"addBase", []string{"'a'", "'b'", "$o"}, "$o[$key] = 'y';", []Overlay{{}}},
		{"addBase", []string{"'a'", "'b'", "$o"}, "$o = [];\nif ($o == $p) {}", []Overlay{{}}},
		{"addBase", []string{"'a'", "'b'", "$o"}, "$other['x'] = 1;", []Overlay{{}}},
		{"addBase", []string{"'a'", "'b'", "$this->overlay()"}, "", []Overlay{{}}},
		{"addBase", []string{"'a'", "'b'", "$o"}, "$o['x'] = 'y';\n$o['x'] .= 'z';", []Overlay{{}}},
		{"addBase", []string{"'a'", "'b'", "$o"}, "$o['x'] = 'y';\n$this->fill($o);", []Overlay{{}}},
		// depends on the request: not one fixed set of fields
		{"addBase", []string{"'a'", "'b'", "$o"}, "$t = $this->request->getPost('o');\nif ($t['type'] === 'set') { $o['set_tag'] = ''; }", []Overlay{{}}},
	}
	for _, c := range cases {
		got := baseCall(c.call, c.args, c.before).Overlay
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s(%v): %+v, want %+v", c.call, c.args, got, c.want)
		}
	}
}

// Claim: the key of an own settings get or set action is read from its
// code, and left empty whenever the code does not show exactly one key on
// the bound model. Bodies as in the 26.7.9 plugin sources (2026-11-18): the
// generic key ($internalModelName, "Client") was wrong for os-freeradius,
// os-siproxd and os-zerotier, whose own actions answer {"client": ...}.
func TestOwnSettingsKeys(t *testing.T) {
	for _, c := range []struct {
		name, body, want string
	}{
		{"os-freeradius client get", `{
        $result = array();
        if ($this->request->isGet()) {
            $mdlClient = new Client();
            $result['client'] = $mdlClient->getNodes();
        }
        return $result;
    }`, "client"},
		{"os-zerotier settings get", `{
        $result = array();
        if ($this->request->isGet()) {
            $mdlZerotier = $this->getModel();
            if (empty($mdlZerotier->localconf->__toString())) {
                $mdlZerotier->localconf = '{}';
            }
            $result = array("zerotier" => $mdlZerotier->getNodes());
        }
        return $result;
    }`, "zerotier"},
		{"core source nat get", `{
        return [
            self::$internalModelName => [
                'general' => $this->getModel()->general->getNodes()
            ]
        ];
    }`, ModelNameRef},
		{"os-stunnel services get", `{
        $result = array();
        $result[static::$internalModelName] = [
            "general" => $this->getModel()->general->getNodes()
        ];
        return $result;
    }`, ModelNameRef},
		{"os-netbird authentication get", `{
        $mdl = new Authentication();
        $result = [
            'authentication' => [
                'managementUrl' => $mdl->managementUrl->getValue(),
                'setupKey' => '',
            ]
        ];
        if (!empty($setupKey)) {
            $result['authentication']['%setupKey'] = substr($setupKey, 0, 5);
        }
        return $result;
    }`, "authentication"},
		{"os-relayd: several keys", `{
        $result = array("result" => "failed");
        if ($node != null) {
            $result['relayd'] = array($nodeType => $node->getNodes());
            $result['status'] = 'ok';
        }
        return $result;
    }`, ""},
		{"os-squid template: not an array", `{
        $result = json_decode($backend->configdRun("proxy download_error_pages"), true);
        if ($result != null) {
            return base64_decode($result['payload']);
        } else {
            return "";
        }
    }`, ""},
		{"variable assigned an expression", `{ $result = $this->load(); $result['x'] = 1; return $result; }`, ""},
		{"variable passed on", `{ $result = ['x' => 1]; $this->fill($result); return $result; }`, ""},
		{"computed key", `{ $result = []; $result[$name] = 1; return $result; }`, ""},
		{"no return", `{ $result['x'] = 1; }`, ""},
		{"one return is not an array", `{ if ($x) { return $this->other(); } return ['x' => 1]; }`, ""},
	} {
		if got := responseKey(c.body); got != c.want {
			t.Errorf("get %s: %q, want %q", c.name, got, c.want)
		}
	}
	for _, c := range []struct {
		name, body, key, class string
	}{
		{"os-freeradius client set", `{
            $mdlClient = new Client();
            $mdlClient->setNodes($this->request->getPost("client"));
            $valMsgs = $mdlClient->performValidation();
    }`, "client", "Client"},
		{"core source nat set: a node below the model", `{
            $mdl = $this->getModel();
            $mdl->general->setNodes($this->request->getPost('filter')['general'] ?? []);
    }`, "filter", ""},
		{"core firmware set: through a variable", `{
        $values = $this->request->getPost(static::$internalModelName);
        unset($values['plugins']);
        $mdl = $this->getModel();
        $mdl->setNodes($values);
    }`, ModelNameRef, ""},
		{"getModel() directly", `{ $this->getModel()->setNodes($this->request->getPost('x')); }`, "x", ""},
		{"os-wol set: an entry, not the model", `{
            $wol = $this->getModel();
            $wolent = $wol->wolentry->Add();
            $wolent->setNodes($this->request->getPost('wake'));
    }`, "", ""},
		{"os-iperf set: $_POST", `{ $interface_name = $_POST['instance']['interface']; }`, "", ""},
		{"two keys", `{ $m = $this->getModel(); $m->a->setNodes($this->request->getPost('a')); $m->b->setNodes($this->request->getPost('b')); }`, "", ""},
		{"argument not from the request", `{ $m = $this->getModel(); $m->setNodes($this->defaults()); }`, "", ""},
		{"model variable assigned twice", `{ $m = new A(); $m = $other; $m->setNodes($this->request->getPost('a')); }`, "", ""},
	} {
		if k, cl := requestKey(c.body); k != c.key || cl != c.class {
			t.Errorf("set %s: %q %q, want %q %q", c.name, k, cl, c.key, c.class)
		}
	}
}

// Claim: the hooks of ApiMutableModelControllerBase a controller overrides
// are found in its own code (save as in os-stunnel's ServicesController),
// not an action with a similar name or a mention in a comment.
func TestHooks(t *testing.T) {
	src := `<?php
namespace OPNsense\Demo\Api;
use OPNsense\Base\ApiMutableModelControllerBase;
class ServicesController extends ApiMutableModelControllerBase
{
    // function getModelNodes() is not overridden here
    protected function save($validateFullModel = false, $disable_validation = false)
    {
        return parent::save($validateFullModel, $disable_validation);
    }
    public function saveAction() { return []; }
}`
	cls := ParseFile(src, "Demo/Api/ServicesController.php")
	if len(cls) != 1 || !reflect.DeepEqual(cls[0].Hooks, []string{"save"}) {
		t.Fatalf("hooks %+v", cls)
	}
}

// the fields a setBaseHook assigns (26.1 DNatController: the audit
// fields), not the ones it only reads or compares
func TestHookSets(t *testing.T) {
	src := `<?php
namespace OPNsense\Demo\Api;
use OPNsense\Base\ApiMutableModelControllerBase;
class RuleController extends ApiMutableModelControllerBase
{
    protected function setBaseHook($node)
    {
        $node->updated->time = sprintf('%0.2f', microtime(true));
        $node->{'max-src'} = '1';
        if ($node->created->time->isEmpty() || $node->enabled == '1') {
            $node->created->time = $node->updated->time;
        }
    }
}`
	cls := ParseFile(src, "Demo/Api/RuleController.php")
	if len(cls) != 1 || !reflect.DeepEqual(cls[0].HookSets, []string{"created.time", "max-src", "updated.time"}) {
		t.Fatalf("hook sets %+v", cls[0].HookSets)
	}
}
