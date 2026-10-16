package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/vilanobeachflorida/racksail/internal/theme"
)

// loadTheme reads a theme from a theme.yaml or JSON file, a theme package
// directory or a zip archive of one, or names a built-in theme.
func loadTheme(arg string) (*theme.Theme, error) {
	if t, ok := theme.Builtin(arg); ok {
		return &t, nil
	}
	info, err := os.Stat(arg)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		pkg, err := theme.ReadPackage(os.DirFS(arg))
		if err != nil {
			return nil, err
		}
		return &pkg.Theme, nil
	}
	if strings.EqualFold(filepath.Ext(arg), ".zip") {
		f, err := os.Open(arg)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		pkg, err := theme.ReadArchive(f, info.Size())
		if err != nil {
			return nil, err
		}
		return &pkg.Theme, nil
	}
	data, err := os.ReadFile(arg)
	if err != nil {
		return nil, err
	}
	return theme.Parse(data)
}

func newThemeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "theme",
		Short: "Check themes, render them as CSS and export the built-in ones",
		Long: "Themes are design tokens and asset files. A theme can be given as a theme.yaml or JSON file, " +
			"a theme package directory, a zip archive of one, or the id of a built-in theme (racksail, " +
			"high-contrast or compact).",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "validate THEME",
		Short: "Check a theme against the schema, for WCAG AA contrast and the files it refers to",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := loadTheme(args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s is valid.\n", t.Name, t.Version)
			return nil
		},
	})
	var layer bool
	css := &cobra.Command{
		Use:   "css [THEME]",
		Short: "Print a theme as CSS custom properties; the default theme without THEME",
		Long: "Prints the token stylesheet the panel loads: the theme, the blocks personal preferences switch on " +
			"and the protected palette. With --layer it prints only the theme, as the panel applies server and " +
			"reseller themes on top of the default one.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t := theme.Default()
			if len(args) == 1 {
				loaded, err := loadTheme(args[0])
				if err != nil {
					return err
				}
				t = *loaded
			}
			out := t.CSS()
			if layer {
				out = t.LayerCSS(nil)
			}
			_, err := fmt.Fprint(cmd.OutOrStdout(), out)
			return err
		},
	}
	css.Flags().BoolVar(&layer, "layer", false, "print only the theme's own layer")
	cmd.AddCommand(css)

	var format string
	export := &cobra.Command{
		Use:   "export THEME",
		Short: "Print a theme as theme.yaml or JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := loadTheme(args[0])
			if err != nil {
				return err
			}
			var data []byte
			switch format {
			case "yaml":
				data, err = t.YAML()
			case "json":
				data, err = t.JSON()
			default:
				return fmt.Errorf("--format is yaml or json, not %q", format)
			}
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
	export.Flags().StringVar(&format, "format", "yaml", "yaml or json")
	cmd.AddCommand(export)

	cmd.AddCommand(&cobra.Command{
		Use:   "spec",
		Short: "Print the built-in themes and the rules themes are checked against, as the web UI uses them",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := theme.UISpec().JSON()
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List the built-in themes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, t := range theme.Builtins() {
				fmt.Fprintf(cmd.OutOrStdout(), "%-14s %s\n", t.ID, t.Description)
			}
			return nil
		},
	})

	var size string
	mark := &cobra.Command{
		Use:   "mark [THEME]",
		Short: "Draw the Racksail mark in the terminal with a theme's console palette",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t := theme.Default()
			if len(args) == 1 {
				loaded, err := loadTheme(args[0])
				if err != nil {
					return err
				}
				t = *loaded
			}
			s := theme.MarkLarge
			switch size {
			case "large":
			case "small":
				s = theme.MarkSmall
			default:
				return fmt.Errorf("--size is small or large, not %q", size)
			}
			depth := theme.DetectColorDepth(os.Getenv)
			_, err := fmt.Fprint(cmd.OutOrStdout(), t.Console.Mark(s, depth))
			return err
		},
	}
	mark.Flags().StringVar(&size, "size", "large", "small or large")
	cmd.AddCommand(mark)
	return cmd
}
