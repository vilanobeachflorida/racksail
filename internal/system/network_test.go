package system

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCheckRulesetAcceptsTableBlocks(t *testing.T) {
	ruleset := `# Managed by Racksail. Changes made here are overwritten.
table inet racksail {
	set ban_ssh_v4 {
		type ipv4_addr
		flags timeout
		timeout 1h
	}
	map ports { type inet_service : verdict; }
	chain input {
		type filter hook input priority filter; policy drop;
		tcp dport 22 accept comment "SSH { not a block } # nor a comment"
		ip saddr @ban_ssh_v4 drop # trailing comment with include in it
	}
}

table ip racksail_nat { chain postrouting { type nat hook postrouting priority srcnat; policy accept; } }
`
	tables, err := CheckRuleset([]byte(ruleset))
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 2 || tables[0].Family != "inet" || tables[0].Name != "racksail" ||
		!slices.Equal(tables[0].Sets, []string{"ban_ssh_v4", "ports"}) || tables[1].Name != "racksail_nat" || len(tables[1].Sets) != 0 {
		t.Errorf("tables = %+v", tables)
	}

	// What the renderer writes passes, with the sets it declares.
	golden, err := os.ReadFile(filepath.Join("testdata", "firewall.nft"))
	if err != nil {
		t.Fatal(err)
	}
	tables, err = CheckRuleset(golden)
	if err != nil || len(tables) != 1 || !slices.Contains(tables[0].Sets, "ban_ssh_v6") || !slices.Contains(tables[0].Sets, "rate_tcp_22_v4") {
		t.Errorf("rendered ruleset = %+v, %v", tables, err)
	}
}

func TestCheckRulesetRefusesAnythingButRacksailTables(t *testing.T) {
	table := "table inet racksail {\n chain input { type filter hook input priority 0; policy drop; }\n}\n"
	for name, ruleset := range map[string]string{
		"empty":                       "  \n# nothing\n",
		"only comments":               "# table inet racksail {}\n",
		"flush ruleset":               "flush ruleset\n" + table,
		"flush ruleset after":         table + "flush ruleset\n",
		"flush on the same line":      "table inet racksail { }; flush ruleset\n",
		"include at the top":          "include \"/etc/shadow\"\n" + table,
		"include in a table":          "table inet racksail {\n include \"/root/.ssh/id_ed25519\"\n}\n",
		"include in a chain":          "table inet racksail {\n chain c {\n  include \"/etc/shadow\"\n }\n}\n",
		"define":                      "define ADMIN = 192.0.2.1\n" + table,
		"define in a table":           "table inet racksail {\n define x = 1\n}\n",
		"redefine":                    "table inet racksail {\n redefine x = 1\n}\n",
		"undefine":                    "table inet racksail {\n undefine x\n}\n",
		"variable":                    "table inet racksail {\n chain c { ip saddr $ADMIN accept }\n}\n",
		"another table":               "table inet filter {\n chain input { policy accept; }\n}\n",
		"table named like ours":       "table inet xracksail { }\n",
		"quoted table name":           "table inet \"racksail\" { }\n",
		"unknown family":              "table foo racksail { }\n",
		"add table":                   "add table inet racksail { }\n",
		"table without a block":       "table inet racksail\n",
		"rule in another table":       table + "add rule inet filter input accept\n",
		"chain in another table":      table + "add chain inet filter backdoor { type filter hook input priority -500; policy accept; }\n",
		"chain without add":           table + "chain inet filter backdoor { type filter hook input priority -500; policy accept; }\n",
		"set in another table":        table + "add set inet filter s { type ipv4_addr; }\n",
		"element in another table":    table + "add element inet filter allow { 0.0.0.0/0 }\n",
		"delete another table":        "delete table inet filter\n" + table,
		"delete ours":                 "delete table inet racksail\n" + table,
		"list ruleset":                "list ruleset\n" + table,
		"unbalanced open":             "table inet racksail {\n chain c {\n}\n",
		"unbalanced close":            table + "}\n",
		"unterminated string":         "table inet racksail {\n chain c { accept comment \"x }\n}\n",
		"string across lines":         "table inet racksail {\n chain c { accept comment \"x\n}\" }\n}\n",
		"backslash":                   "table inet racksail {\n chain c { accept \\\n }\n}\n",
		"carriage return":             "table inet racksail {\r\n}\r\n",
		"control character":           "table inet racksail {\x00}\n",
		"not text":                    "table inet racksail { \xff }\n",
		"text after a table":          "table inet racksail { } table ip racksail2 { }\n",
		"statement before the header": "flush table inet racksail; table inet racksail { }\n",
	} {
		if _, err := CheckRuleset([]byte(ruleset)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: should be refused: %v", name, err)
		}
	}
	// The refusal doesn't quote more than a line of the ruleset.
	_, err := CheckRuleset([]byte("add rule inet filter input " + strings.Repeat("ip saddr 192.0.2.1 ", 50) + "accept\n"))
	if err == nil || len(err.Error()) > 300 {
		t.Errorf("refusal = %v", err)
	}
}

func FuzzCheckRuleset(f *testing.F) {
	f.Add([]byte("table inet racksail {\n set s { type ipv4_addr; }\n chain c { accept comment \"{\" }\n}\n"))
	f.Add([]byte("flush ruleset\n"))
	f.Add([]byte("table inet racksail { include \"x\" }"))
	f.Fuzz(func(t *testing.T, data []byte) {
		tables, err := CheckRuleset(data)
		if err != nil {
			return
		}
		if len(tables) == 0 {
			t.Fatal("an accepted ruleset defines a table")
		}
		for _, tb := range tables {
			if !strings.HasPrefix(tb.Name, "racksail") {
				t.Fatalf("accepted a table that isn't Racksail's: %+v", tb)
			}
		}
		tokens, err := nftTokens(data)
		if err != nil {
			t.Fatalf("accepted a ruleset that doesn't tokenize: %v", err)
		}
		for _, tok := range tokens {
			if tok.kind == nftWord && slices.Contains(nftDirectives, tok.text) {
				t.Fatalf("accepted a ruleset with %s", tok.text)
			}
		}
	})
}
