package web

import (
	"net/url"
	"strings"
	"testing"
)

func TestCreateRoleMembership(t *testing.T) {
	form := url.Values{
		"name":   {"app"},
		"inrole": {"readers", "writers"},
		"admins": {"boss"},
	}
	got, err := buildCreateRoleForm(form)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`IN ROLE "readers", "writers"`, `ADMIN "boss"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\n    ROLE ") {
		t.Errorf("unexpected ROLE clause in:\n%s", got)
	}
}

func TestAlterRoleMembership(t *testing.T) {
	tests := []struct {
		name string
		form url.Values
		want []string
		err  bool
	}{
		{
			name: "grant and revoke memberof",
			form: url.Values{"name": {"app"}, "memberof_orig": {"a,b"}, "memberof": {"b", "c"}},
			want: []string{`GRANT "c" TO "app";`, `REVOKE "a" FROM "app";`},
		},
		{
			name: "members direction",
			form: url.Values{"name": {"app"}, "members_orig": {"x"}, "members": {"y"}},
			want: []string{`GRANT "app" TO "y";`, `REVOKE "app" FROM "x";`},
		},
		{
			name: "unchanged",
			form: url.Values{"name": {"app"}, "memberof_orig": {"a"}, "memberof": {"a"}},
			err:  true,
		},
		{
			name: "no baseline emits nothing",
			form: url.Values{"name": {"app"}, "memberof": {"a"}},
			err:  true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildAlterRoleForm(tc.form)
			if tc.err {
				if err == nil {
					t.Fatalf("want error, got:\n%s", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
		})
	}
}
