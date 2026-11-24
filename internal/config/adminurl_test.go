package config

import "testing"

func TestGuessDataAPIURL(t *testing.T) {
	cases := map[string]string{
		"https://example.com/cgi-bin/mt/mt.cgi":                  "https://example.com/cgi-bin/mt/mt-data-api.cgi",
		"https://example.com/cgi-bin/mt/mt.cgi?__mode=dashboard": "https://example.com/cgi-bin/mt/mt-data-api.cgi",
		"  https://example.com/mt/admin#top ":                    "https://example.com/mt/mt-data-api.cgi", // MT 8 以降の PSGI
		"https://example.com/cgi-bin/mt/":                        "https://example.com/cgi-bin/mt/mt-data-api.cgi",
		"http://localhost:8080/mt/admin?__mode=view&_type=entry": "http://localhost:8080/mt/mt-data-api.cgi",
		"example.com/mt.cgi":                                     "", // スキームが無ければ推測しない
		"":                                                       "",
	}
	for in, want := range cases {
		if got := GuessDataAPIURL(in); got != want {
			t.Errorf("GuessDataAPIURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGuessAdminURL(t *testing.T) {
	cases := map[string]string{
		"https://example.com/cgi-bin/mt/mt-data-api.cgi":     "https://example.com/cgi-bin/mt/mt.cgi",
		"https://example.com/cgi-bin/mt/mt-data-api.cgi/v7/": "https://example.com/cgi-bin/mt/mt.cgi",
		"https://example.com/api/endpoint.cgi":               "", // 名前を変えてあれば推測しない
	}
	for in, want := range cases {
		if got := GuessAdminURL(in); got != want {
			t.Errorf("GuessAdminURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveAdminURL(t *testing.T) {
	s := New(NewMemStorage(), MapEnv(map[string]string{}))
	if err := s.Save(&File{Current: "p", Profiles: map[string]*Profile{"p": {BaseURL: "https://e.example/mt-data-api.cgi", AdminURL: "https://e.example/mt.cgi"}}}); err != nil {
		t.Fatal(err)
	}
	r, err := s.Resolve(Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if r.AdminURL != "https://e.example/mt.cgi" {
		t.Errorf("AdminURL = %q", r.AdminURL)
	}
	s = New(NewMemStorage(), MapEnv(map[string]string{"MTB_BASE_URL": "https://e.example/mt-data-api.cgi", "MTB_ADMIN_URL": "https://e.example/admin?__mode=x"}))
	if r, err = s.Resolve(Overrides{}); err != nil {
		t.Fatal(err)
	}
	if r.AdminURL != "https://e.example/admin" {
		t.Errorf("MTB_ADMIN_URL は問い合わせを落として使う: %q", r.AdminURL)
	}
}
