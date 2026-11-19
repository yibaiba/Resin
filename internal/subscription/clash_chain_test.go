package subscription

import (
	"reflect"
	"regexp"
	"testing"

	"github.com/Resinat/Resin/internal/node"
)

func TestParseGeneralSubscription_ClashYAML_ChainRolesFromGroups(t *testing.T) {
	data := []byte(`
proxies:
  - name: hop-jp
    type: ss
    server: 1.2.3.4
    port: 443
    cipher: aes-256-gcm
    password: hop-pass
  - name: land-home
    type: http
    server: 5.6.7.8
    port: 8080
    username: user
    password: land-pass
    dialer-proxy: hops
  - name: dummy
    type: direct
proxy-groups:
  - name: hops
    type: select
    proxies:
      - dummy
      - hop-jp
  - name: landings
    type: select
    proxies:
      - land-home
`)
	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("parsed %d nodes, want 2", len(nodes))
	}
	byTag := map[string]ParsedNode{}
	for _, n := range nodes {
		byTag[n.Tag] = n
	}
	if !reflect.DeepEqual(byTag["hop-jp"].ExtraTags, []string{RoleTagHop}) {
		t.Fatalf("hop extra tags = %v, want [%s]", byTag["hop-jp"].ExtraTags, RoleTagHop)
	}
	if !reflect.DeepEqual(byTag["land-home"].ExtraTags, []string{RoleTagLanding}) {
		t.Fatalf("landing extra tags = %v, want [%s]", byTag["land-home"].ExtraTags, RoleTagLanding)
	}
}

func TestParseGeneralSubscription_ClashYAML_LandingRegionTags(t *testing.T) {
	data := []byte(`
proxies:
  - name: hop-jp
    type: ss
    server: 1.2.3.4
    port: 443
    cipher: aes-256-gcm
    password: hop-pass
  - name: 日本-4% 1.2.3.4
    type: http
    server: 5.6.7.8
    port: 8080
    username: user
    password: land-pass
    dialer-proxy: hop-jp
  - name: 印度-1% 9.9.9.9
    type: http
    server: 6.6.6.6
    port: 8080
    username: user
    password: land-pass
    dialer-proxy: hop-jp
  - name: 印度尼西亚-8% 8.8.8.8
    type: http
    server: 7.7.7.7
    port: 8080
    username: user
    password: land-pass
    dialer-proxy: hop-jp
`)
	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	byTag := map[string]ParsedNode{}
	for _, n := range nodes {
		byTag[n.Tag] = n
	}
	wantJP := []string{RoleTagLanding, RegionTag("日本")}
	if !reflect.DeepEqual(byTag["日本-4% 1.2.3.4"].ExtraTags, wantJP) {
		t.Fatalf("japan extra tags = %v, want %v", byTag["日本-4% 1.2.3.4"].ExtraTags, wantJP)
	}
	if !reflect.DeepEqual(byTag["印度-1% 9.9.9.9"].ExtraTags, []string{RoleTagLanding, RegionTag("印度")}) {
		t.Fatalf("india extra tags = %v", byTag["印度-1% 9.9.9.9"].ExtraTags)
	}
	if !reflect.DeepEqual(byTag["印度尼西亚-8% 8.8.8.8"].ExtraTags, []string{RoleTagLanding, RegionTag("印度尼西亚")}) {
		t.Fatalf("indonesia extra tags = %v", byTag["印度尼西亚-8% 8.8.8.8"].ExtraTags)
	}
}

func TestLandingRegionLabelAndFilter(t *testing.T) {
	if got := LandingRegionLabel("日本-4% 51.241.112.99"); got != "日本" {
		t.Fatalf("japan label = %q", got)
	}
	if got := LandingRegionLabel("公益-DE_speednode"); got != "" {
		t.Fatalf("hop-like label = %q, want empty", got)
	}
	in := RegionFilterPattern("印度")
	id := RegionFilterPattern("印度尼西亚")
	if in == id {
		t.Fatal("india and indonesia filters collided")
	}
	if !regexp.MustCompile(in).MatchString("联合国公益/" + RegionTag("印度")) {
		t.Fatalf("india filter %q should match exact tag", in)
	}
	if regexp.MustCompile(in).MatchString("联合国公益/" + RegionTag("印度尼西亚")) {
		t.Fatalf("india filter %q matched indonesia", in)
	}
}

func TestParseGeneralSubscription_ClashYAML_ChainRolesWithoutGroups(t *testing.T) {
	data := []byte(`
proxies:
  - name: hop-de
    type: ss
    server: 9.9.9.9
    port: 443
    cipher: aes-256-gcm
    password: hop-pass
  - name: land-it
    type: http
    server: 8.8.8.8
    port: 8080
    username: user
    password: land-pass
    dialer-proxy: hop-de
`)
	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("parsed %d nodes, want 2", len(nodes))
	}
	byTag := map[string]ParsedNode{}
	for _, n := range nodes {
		byTag[n.Tag] = n
	}
	if !reflect.DeepEqual(byTag["hop-de"].ExtraTags, []string{RoleTagHop}) {
		t.Fatalf("hop extra tags = %v", byTag["hop-de"].ExtraTags)
	}
	if !reflect.DeepEqual(byTag["land-it"].ExtraTags, []string{RoleTagLanding}) {
		t.Fatalf("landing extra tags = %v", byTag["land-it"].ExtraTags)
	}
}

func TestParseGeneralSubscription_ClashYAML_NoRolesWhenNotMixed(t *testing.T) {
	onlyHops := []byte(`
proxies:
  - name: hop-only
    type: ss
    server: 1.1.1.1
    port: 443
    cipher: aes-256-gcm
    password: p
`)
	nodes, err := ParseGeneralSubscription(onlyHops)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("parsed %d nodes, want 1", len(nodes))
	}
	if len(nodes[0].ExtraTags) != 0 {
		t.Fatalf("hop-only extra tags = %v, want none", nodes[0].ExtraTags)
	}

	onlyLandings := []byte(`
proxies:
  - name: land-only
    type: http
    server: 2.2.2.2
    port: 8080
    username: u
    password: p
    dialer-proxy: missing-hop
`)
	nodes, err = ParseGeneralSubscription(onlyLandings)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("parsed %d landing nodes, want 1", len(nodes))
	}
	if len(nodes[0].ExtraTags) != 0 {
		t.Fatalf("land-only extra tags = %v, want none", nodes[0].ExtraTags)
	}
}

func TestParseGeneralSubscription_ClashJSON_ChainRolesFromGroups(t *testing.T) {
	data := []byte(`{
		"proxies": [
			{"name":"hop-nl","type":"ss","server":"1.2.3.4","port":443,"cipher":"aes-256-gcm","password":"p"},
			{"name":"land-fr","type":"http","server":"5.6.7.8","port":8080,"username":"u","password":"p","dialer-proxy":"hops"}
		],
		"proxy-groups": [
			{"name":"hops","type":"select","proxies":["hop-nl"]}
		]
	}`)
	nodes, err := ParseGeneralSubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("parsed %d nodes, want 2", len(nodes))
	}
	byTag := map[string]ParsedNode{}
	for _, n := range nodes {
		byTag[n.Tag] = n
	}
	if !reflect.DeepEqual(byTag["hop-nl"].ExtraTags, []string{RoleTagHop}) {
		t.Fatalf("hop extra tags = %v", byTag["hop-nl"].ExtraTags)
	}
	if !reflect.DeepEqual(byTag["land-fr"].ExtraTags, []string{RoleTagLanding}) {
		t.Fatalf("landing extra tags = %v", byTag["land-fr"].ExtraTags)
	}
}

func TestCountChainRoles(t *testing.T) {
	mn := NewManagedNodes()
	mn.StoreNode(node.HashFromRawOptions([]byte(`{"type":"ss","server":"1.1.1.1","tag":"a"}`)), ManagedNode{Tags: []string{"a", RoleTagHop}})
	mn.StoreNode(node.HashFromRawOptions([]byte(`{"type":"http","server":"2.2.2.2","tag":"b"}`)), ManagedNode{Tags: []string{"b", RoleTagLanding}})
	mn.StoreNode(node.HashFromRawOptions([]byte(`{"type":"ss","server":"3.3.3.3","tag":"c"}`)), ManagedNode{Tags: []string{"c", RoleTagLanding}, Evicted: true})
	hops, landings := CountChainRoles(mn)
	if hops != 1 || landings != 1 {
		t.Fatalf("CountChainRoles = %d hop, %d landing, want 1, 1", hops, landings)
	}
}
