package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type staticToken string

func (s staticToken) token(context.Context) (string, error) { return string(s), nil }

func TestPickProperty(t *testing.T) {
	page, _ := url.Parse("https://www.example.com/blog/post")
	sites := []gscSite{
		{SiteURL: "https://www.example.com/", PermissionLevel: "siteOwner"},
		{SiteURL: "https://www.example.com/blog/", PermissionLevel: "siteFullUser"},
		{SiteURL: "https://other.com/", PermissionLevel: "siteOwner"},
	}
	if p, _ := pickProperty(sites, page); p != "https://www.example.com/blog/" {
		t.Errorf("longest prefix: %s", p)
	}
	sites = append(sites, gscSite{SiteURL: "sc-domain:example.com", PermissionLevel: "siteOwner"})
	if p, _ := pickProperty(sites, page); p != "sc-domain:example.com" {
		t.Errorf("domain property should win: %s", p)
	}
	if _, ok := pickProperty([]gscSite{{SiteURL: "sc-domain:example.com", PermissionLevel: "siteUnverifiedUser"}}, page); ok {
		t.Error("unverified property used")
	}
	if _, ok := pickProperty([]gscSite{{SiteURL: "sc-domain:ample.com", PermissionLevel: "siteOwner"}}, page); ok {
		t.Error("suffix match without a dot boundary")
	}
}

func TestServiceAccountAssertion(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	path := filepath.Join(t.TempDir(), "sa.json")
	b, _ := json.Marshal(map[string]string{"client_email": "bot@proj.iam.gserviceaccount.com", "private_key": pemKey, "token_uri": "https://oauth2.googleapis.com/token"})
	os.WriteFile(path, b, 0o600)
	sa, err := loadServiceAccount(path)
	if err != nil {
		t.Fatal(err)
	}
	jwt, err := sa.assertion(time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt %q", jwt)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatal("bad signature:", err)
	}
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c map[string]any
	json.Unmarshal(claims, &c)
	if c["iss"] != "bot@proj.iam.gserviceaccount.com" || c["scope"] != gscScope || c["exp"].(float64)-c["iat"].(float64) != 3600 {
		t.Errorf("claims %v", c)
	}
}

func TestPostTokenErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("refresh_token") == "good" {
			io.WriteString(w, `{"access_token":"at","expires_in":3599}`)
			return
		}
		w.WriteHeader(400)
		io.WriteString(w, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`)
	}))
	defer srv.Close()
	if tok, ttl, err := postToken(context.Background(), srv.URL, url.Values{"refresh_token": {"good"}}); err != nil || tok != "at" || ttl != 3599*time.Second {
		t.Fatalf("%q %v %v", tok, ttl, err)
	}
	_, _, err := postToken(context.Background(), srv.URL, url.Values{"refresh_token": {"old"}})
	if err == nil || !strings.Contains(err.Error(), "sitecheck auth google") {
		t.Fatalf("expired token error should say how to fix: %v", err)
	}
}

// fakeSearchAPIs serves just enough of Search Console and Bing Webmaster.
func fakeSearchAPIs(t *testing.T, gscSites, bingSites string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/gsc/sites", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"message":"bad token"}}`)
			return
		}
		io.WriteString(w, gscSites)
	})
	mux.HandleFunc("/gsc/sites/sc-domain:example.com/searchAnalytics/query", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["dimensions"] == nil {
			io.WriteString(w, `{"rows":[{"clicks":120,"impressions":4000,"ctr":0.03,"position":11.2}]}`)
			return
		}
		io.WriteString(w, `{"rows":[
			{"keys":["screenplay editor"],"clicks":40,"impressions":900,"ctr":0.044,"position":4.1},
			{"keys":["read screenplay aloud"],"clicks":3,"impressions":300,"ctr":0.01,"position":8.5},
			{"keys":["final draft online"],"clicks":0,"impressions":5,"ctr":0,"position":30}]}`)
	})
	mux.HandleFunc("/gsc/sites/sc-domain:example.com/sitemaps", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{}`)
	})
	mux.HandleFunc("/inspect", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"inspectionResult":{"indexStatusResult":{"verdict":"PASS","coverageState":"Submitted and indexed",
			"robotsTxtState":"ALLOWED","pageFetchState":"SUCCESSFUL","lastCrawlTime":"2026-11-01T10:00:00Z","crawledAs":"MOBILE",
			"googleCanonical":"https://example.com/","userCanonical":"https://www.example.com/"}}}`)
	})
	mux.HandleFunc("/bing/GetUserSites", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != "bingkey" {
			w.WriteHeader(400)
			io.WriteString(w, `{"ErrorCode":3,"Message":"InvalidApiKey"}`)
			return
		}
		io.WriteString(w, bingSites)
	})
	mux.HandleFunc("/bing/GetLinkCounts", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "0" {
			io.WriteString(w, `{"d":{"Links":[{"Count":7,"Url":"https://www.example.com/"}],"TotalPages":2}}`)
		} else {
			io.WriteString(w, `{"d":{"Links":[{"Count":3,"Url":"https://www.example.com/pricing"}],"TotalPages":2}}`)
		}
	})
	mux.HandleFunc("/bing/GetUrlLinks", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"d":{"Details":[{"AnchorText":"Scriptroom","Url":"https://blog.one.com/post"},{"AnchorText":"","Url":"https://www.two.org/list"}],"TotalPages":1}}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	oldG, oldI, oldB := gscBase, gscIndex, bingBase
	gscBase, gscIndex, bingBase = srv.URL+"/gsc", srv.URL+"/inspect", srv.URL+"/bing"
	t.Cleanup(func() { gscBase, gscIndex, bingBase = oldG, oldI, oldB })
	return srv
}

func TestSearchOwnedSite(t *testing.T) {
	fakeSearchAPIs(t,
		`{"siteEntry":[{"siteUrl":"sc-domain:example.com","permissionLevel":"siteOwner"}]}`,
		`{"d":[{"Url":"https://www.example.com/","IsVerified":true}]}`)
	page, _ := url.Parse("https://www.example.com/")
	r := &report{Owned: new(bool)}
	now := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	checkGSC(context.Background(), r, staticToken("tok"), page, now)
	checkBing(context.Background(), r, "bingkey", page)
	got := findings(r, "search")
	if g := got["Google index status"]; g.Level != pass || !strings.Contains(g.Detail, "2026-11-01 as mobile") {
		t.Errorf("index: %+v", g)
	}
	if g := got["Google canonical"]; g.Level != warn {
		t.Errorf("canonical mismatch: %+v", g)
	}
	if g := got["Striking-distance queries"]; !strings.Contains(g.Detail, "read screenplay aloud") || strings.Contains(g.Detail, "final draft online") {
		t.Errorf("striking distance: %+v", g)
	}
	if g := got["Google search, this page"]; !strings.Contains(g.Detail, "43 clicks, 1205 impressions across 3 queries") {
		t.Errorf("page totals: %+v", g)
	}
	if g := got["Search Console sitemaps"]; g.Level != warn {
		t.Errorf("no sitemaps: %+v", g)
	}
	if g := got["Backlinks (Bing)"]; !strings.Contains(g.Detail, "10 inbound links to 2 pages") || !strings.Contains(g.Detail, "7 to this page") {
		t.Errorf("bing totals: %+v", g)
	}
	if g := got["Linking pages (Bing)"]; !strings.Contains(g.Detail, "2 referring domains") {
		t.Errorf("bing links: %+v", g)
	}
	if !*r.Owned || len(r.VerifiedIn) != 2 {
		t.Errorf("ownership: %v %v", *r.Owned, r.VerifiedIn)
	}
}

func TestSearchSomeoneElsesSite(t *testing.T) {
	fakeSearchAPIs(t,
		`{"siteEntry":[{"siteUrl":"sc-domain:mine.com","permissionLevel":"siteOwner"}]}`,
		`{"d":[{"Url":"https://mine.com/","IsVerified":true}]}`)
	page, _ := url.Parse("https://www.example.com/")
	r := &report{Owned: new(bool)}
	checkGSC(context.Background(), r, staticToken("tok"), page, time.Now())
	checkBing(context.Background(), r, "bingkey", page)
	if *r.Owned {
		t.Fatal("someone else's site marked as owned")
	}
	for _, f := range r.Findings {
		if f.Level > info {
			t.Errorf("not owning a site should not warn: %+v", f)
		}
	}
}

func TestBingKeyNotLeakedInErrors(t *testing.T) {
	bingBase = "http://127.0.0.1:1/unreachable"
	defer func() { bingBase = "https://ssl.bing.com/webmaster/api.svc/json" }()
	var out any
	err := bingCall(context.Background(), "secret-key-123", "GetUserSites", url.Values{}, &out)
	if err == nil || strings.Contains(err.Error(), "secret-key-123") {
		t.Fatalf("error leaks key or is nil: %v", err)
	}
}
