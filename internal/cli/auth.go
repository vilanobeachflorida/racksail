package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/vilanobeachflorida/racksail/internal/api"
	"github.com/vilanobeachflorida/racksail/internal/auth"
)

func newLoginCommand(opts *Options) *cobra.Command {
	var tokenStdin bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in to a panel with an API token, and keep it in a profile",
		Long: "Asks for an API token, checks it with the panel and keeps it in a profile, which becomes the current one, " +
			"so later commands call that panel with it. Make the token in the panel, under Your account, API tokens: " +
			"it can only do what its permissions and accounts allow, and it can't change how anyone signs in. " +
			"The profile is named after the panel's host unless --profile names it; logging in again with a profile's name replaces its token. " +
			"--token-stdin reads the token from standard input, for scripts.\n\n" +
			"Profiles are kept in a file only you can read, racksail/cli.json in your configuration directory, or where " + ConfigEnv + " says.",
		Example: "  racksail login --server https://panel.example.com\n" +
			"  racksail login --server https://panel.example.com --profile work --token-stdin < token.txt",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return login(cmd, opts, tokenStdin)
		},
	}
	AddFlags(cmd, opts)
	cmd.Flags().BoolVar(&tokenStdin, "token-stdin", false, "read the token from standard input instead of asking for it")
	return cmd
}

func login(cmd *cobra.Command, opts *Options, tokenStdin bool) error {
	if err := opts.checkOutput(); err != nil {
		return err
	}
	profiles, err := LoadProfiles()
	if err != nil {
		return err
	}
	server := opts.Server
	if server == "" {
		server = os.Getenv(ServerEnv)
	}
	name := opts.Profile
	if name == "" {
		name = os.Getenv(ProfileEnv)
	}
	existing := profiles.Profiles[name]
	if server == "" && existing != nil {
		server = existing.Server
	}
	if server == "" {
		return errors.New("give the panel's address with --server, such as racksail login --server https://panel.example.com")
	}
	if server, err = NormalizeServer(server); err != nil {
		return err
	}
	if name == "" {
		name = ProfileName(server)
		existing = profiles.Profiles[name]
	}
	caFile := opts.CAFile
	if caFile == "" && existing != nil && sameServer(existing.Server, server) {
		caFile = existing.CAFile
	}
	token, err := readToken(cmd, server, tokenStdin)
	if err != nil {
		return err
	}
	c, err := NewClient(server, token, caFile, name)
	if err != nil {
		return err
	}
	var session api.Session
	if _, err := c.Get(cmd.Context(), api.Prefix+"/session", &session); err != nil {
		return err
	}
	if session.Token == nil {
		return fmt.Errorf("%s didn't take the token as an API token; make one in the panel, under Your account, API tokens", server)
	}
	profiles.Profiles[name] = &Profile{
		Server: server, CAFile: caFile, Token: token,
		User: session.User.Username, TokenName: session.Token.Name, SavedAt: time.Now().UTC(),
	}
	profiles.Current = name
	if err := profiles.Save(); err != nil {
		return err
	}
	if opts.Output != "table" {
		raw, err := json.Marshal(map[string]any{"profile": name, "server": server, "user": session.User.Username, "token": session.Token.Name})
		if err != nil {
			return err
		}
		return Write(cmd.OutOrStdout(), raw, opts.Output)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Signed in to %s as %s with the token %q.\nSaved as the profile %s, which is now the current one.\n",
		server, session.User.Username, session.Token.Name, name)
	return nil
}

// readToken reads the API token: from standard input with --token-stdin,
// or by asking for it without showing what's typed.
func readToken(cmd *cobra.Command, server string, fromStdin bool) (string, error) {
	in := cmd.InOrStdin()
	var token string
	switch f, isFile := in.(*os.File); {
	case fromStdin:
		line, err := bufio.NewReader(io.LimitReader(in, 4096)).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		token = line
	case isFile && term.IsTerminal(int(f.Fd())):
		fmt.Fprintf(cmd.ErrOrStderr(), "Paste an API token for %s (make one in the panel, under Your account, API tokens): ", server)
		raw, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(cmd.ErrOrStderr())
		if err != nil {
			return "", err
		}
		token = string(raw)
	default:
		return "", errors.New("there's no terminal to ask for the token on; pass it on standard input with --token-stdin")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", errors.New("no token was given")
	}
	if !strings.HasPrefix(token, auth.TokenPrefix) {
		return "", fmt.Errorf("that isn't an API token, which starts with %s; make one in the panel, under Your account, API tokens", auth.TokenPrefix)
	}
	return token, nil
}

func newLogoutCommand(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Forget a profile and its API token",
		Long: "Removes the current profile, or the one --profile names, with its API token. The token itself still works until it expires " +
			"or someone revokes it in the panel, under Your account, API tokens.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			profiles, err := LoadProfiles()
			if err != nil {
				return err
			}
			name := opts.Profile
			if name == "" {
				name = os.Getenv(ProfileEnv)
			}
			name, prof, ok := profiles.Get(name)
			if !ok {
				if name == "" {
					return errors.New("there's no current profile; racksail profile list shows the profiles")
				}
				return fmt.Errorf("there's no profile called %s; racksail profile list shows them", name)
			}
			delete(profiles.Profiles, name)
			if profiles.Current == name {
				profiles.Current = ""
			}
			if err := profiles.Save(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed the profile %s for %s. Its token still works until it expires or someone revokes it in the panel.\n", name, prof.Server)
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.Profile, "profile", "", "profile to remove instead of the current one")
	cmd.RegisterFlagCompletionFunc("profile", completeProfiles)
	return cmd
}

func newWhoamiCommand(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "Show who you're signed in as, and what you may do",
		Long: "Asks the panel who the token or session signs in as: the user, the API token, where the user belongs, " +
			"the permissions their roles give, the accounts their grants name and the modules that are on.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := opts.Connect()
			if err != nil {
				return err
			}
			var raw json.RawMessage
			if _, err := c.Get(cmd.Context(), api.Prefix+"/session", &raw); err != nil {
				return err
			}
			if opts.Output != "table" {
				return Write(cmd.OutOrStdout(), raw, opts.Output)
			}
			var s api.Session
			if err := json.Unmarshal(raw, &s); err != nil {
				return err
			}
			printSession(cmd.OutOrStdout(), c, &s)
			return nil
		},
	}
	AddFlags(cmd, opts)
	return cmd
}

func printSession(w io.Writer, c *Client, s *api.Session) {
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	who := s.User.Username
	if who == "" {
		who = s.User.Name
	} else if s.User.Name != "" && s.User.Name != who {
		who += " (" + s.User.Name + ")"
	}
	if s.User.Email != "" {
		who += " <" + s.User.Email + ">"
	}
	fmt.Fprintf(tw, "User:\t%s\n", who)
	panel := c.Server
	if c.Profile != "" {
		panel += " (profile " + c.Profile + ")"
	}
	fmt.Fprintf(tw, "Panel:\t%s\n", panel)
	switch {
	case s.Token != nil:
		fmt.Fprintf(tw, "Signed in with:\tthe API token %q\n", s.Token.Name)
	case s.Dev:
		fmt.Fprintf(tw, "Signed in with:\tnothing; racksail dev signs everyone in as its developer\n")
	default:
		fmt.Fprintf(tw, "Signed in with:\ta session\n")
	}
	home := string(s.User.Home.Level)
	if s.User.Home.ID != "" {
		home += ":" + string(s.User.Home.ID)
	}
	fmt.Fprintf(tw, "Belongs to:\t%s\n", home)
	if len(s.Permissions) > 0 {
		fmt.Fprintf(tw, "Permissions:\t%s\n", strings.Join(s.Permissions, ", "))
	}
	if len(s.Accounts) > 0 {
		var names []string
		for _, a := range s.Accounts {
			names = append(names, a.Slug+" ("+string(a.ID)+")")
		}
		fmt.Fprintf(tw, "Accounts:\t%s\n", strings.Join(names, ", "))
	}
	if len(s.Modules) > 0 {
		var mods []string
		for id, state := range s.Modules {
			mods = append(mods, id+" ("+string(state)+")")
		}
		sort.Strings(mods)
		fmt.Fprintf(tw, "Modules:\t%s\n", strings.Join(mods, ", "))
	}
	if s.Impersonation != nil {
		fmt.Fprintf(tw, "Viewed by:\t%s, until %s\n", s.Impersonation.Name, s.Impersonation.EndsAt.Local().Format("15:04"))
	}
	if s.ExpiresAt != nil {
		fmt.Fprintf(tw, "Ends:\t%s\n", s.ExpiresAt.Local().Format("2006-01-02 15:04"))
	}
	tw.Flush()
}

func newProfileCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "profile",
		Aliases: []string{"profiles"},
		Short:   "List, switch and remove the panels you've signed in to",
		Long: "Profiles keep a panel's address, the certificate to trust for it and an API token, so commands can call it. " +
			"racksail login adds them; the current one is used unless --profile or " + ProfileEnv + " names another.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	var output string
	list := &cobra.Command{
		Use:   "list",
		Short: "List the profiles",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			profiles, err := LoadProfiles()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if output == "json" || output == "yaml" {
				type row struct {
					Name      string    `json:"name"`
					Server    string    `json:"server"`
					User      string    `json:"user,omitempty"`
					TokenName string    `json:"tokenName,omitempty"`
					Current   bool      `json:"current"`
					SavedAt   time.Time `json:"savedAt,omitzero"`
				}
				rows := []row{}
				for _, name := range profiles.Names() {
					p := profiles.Profiles[name]
					rows = append(rows, row{name, p.Server, p.User, p.TokenName, name == profiles.Current, p.SavedAt})
				}
				raw, err := json.Marshal(rows)
				if err != nil {
					return err
				}
				return Write(out, raw, output)
			}
			if len(profiles.Profiles) == 0 {
				fmt.Fprintln(out, "No profiles yet: sign in to a panel with racksail login --server ADDRESS.")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 8, 2, ' ', 0)
			fmt.Fprintln(tw, "\tNAME\tSERVER\tUSER\tTOKEN")
			for _, name := range profiles.Names() {
				p := profiles.Profiles[name]
				mark := ""
				if name == profiles.Current {
					mark = "*"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", mark, name, p.Server, p.User, p.TokenName)
			}
			return tw.Flush()
		},
	}
	list.Flags().StringVarP(&output, "output", "o", "table", "output format: table, json or yaml")
	use := &cobra.Command{
		Use:               "use <name>",
		Short:             "Make a profile the current one",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeProfiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			profiles, err := LoadProfiles()
			if err != nil {
				return err
			}
			p, ok := profiles.Profiles[args[0]]
			if !ok {
				return fmt.Errorf("there's no profile called %s; racksail profile list shows them", args[0])
			}
			profiles.Current = args[0]
			if err := profiles.Save(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Commands now call %s, as the profile %s.\n", p.Server, args[0])
			return nil
		},
	}
	remove := &cobra.Command{
		Use:               "remove <name>",
		Aliases:           []string{"rm"},
		Short:             "Remove a profile and its API token",
		Long:              "Removes a profile with its API token. The token itself still works until it expires or someone revokes it in the panel.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeProfiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			profiles, err := LoadProfiles()
			if err != nil {
				return err
			}
			if _, ok := profiles.Profiles[args[0]]; !ok {
				return fmt.Errorf("there's no profile called %s; racksail profile list shows them", args[0])
			}
			delete(profiles.Profiles, args[0])
			if profiles.Current == args[0] {
				profiles.Current = ""
			}
			if err := profiles.Save(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed the profile %s.\n", args[0])
			return nil
		},
	}
	cmd.AddCommand(list, use, remove)
	return cmd
}

func completeProfiles(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	profiles, err := LoadProfiles()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, name := range profiles.Names() {
		out = append(out, name+"\t"+profiles.Profiles[name].Server)
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}
